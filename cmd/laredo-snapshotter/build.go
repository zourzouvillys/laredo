package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awskinesis "github.com/aws/aws-sdk-go-v2/service/kinesis"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	fanout "github.com/zourzouvillys/laredo/client/fanout"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/awsx"
	"github.com/zourzouvillys/laredo/snapshotter/config"
	localdest "github.com/zourzouvillys/laredo/snapshotter/dest/local"
	s3dest "github.com/zourzouvillys/laredo/snapshotter/dest/s3"
	kinesissink "github.com/zourzouvillys/laredo/snapshotter/event/kinesis"
	snssink "github.com/zourzouvillys/laredo/snapshotter/event/sns"
	sqssink "github.com/zourzouvillys/laredo/snapshotter/event/sqs"
	"github.com/zourzouvillys/laredo/snapshotter/fanoutsub"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
	"github.com/zourzouvillys/laredo/snapshotter/format/protobuf"
)

// awsConfigCache memoizes resolved aws.Config per (profile, region).
type awsConfigCache struct {
	creds map[string]config.Credential
	cache map[string]aws.Config
}

func newAWSConfigCache(creds map[string]config.Credential) *awsConfigCache {
	return &awsConfigCache{creds: creds, cache: map[string]aws.Config{}}
}

func (c *awsConfigCache) get(ctx context.Context, profileName, region string) (aws.Config, error) {
	p := awsx.Profile{Type: "ambient"}
	if profileName != "" {
		cr, ok := c.creds[profileName]
		if !ok {
			return aws.Config{}, fmt.Errorf("unknown credentials profile %q", profileName)
		}
		p = awsx.Profile{Type: cr.Type, Region: cr.Region, RoleARN: cr.RoleARN, ExternalID: cr.ExternalID, SessionName: cr.SessionName}
		if p.Type == "" {
			p.Type = "ambient"
		}
	}
	if region != "" {
		p.Region = region
	}
	key := profileName + "|" + p.Region
	if cfg, ok := c.cache[key]; ok {
		return cfg, nil
	}
	cfg, err := awsx.Config(ctx, p)
	if err != nil {
		return aws.Config{}, err
	}
	c.cache[key] = cfg
	return cfg, nil
}

// buildWriters turns the parsed config into one Writer per table, wiring concrete
// destinations, formats, and event sinks (resolving AWS credentials per action).
func buildWriters(ctx context.Context, cfg *config.Config) ([]*snapshotter.Writer, error) {
	awsCfg := newAWSConfigCache(cfg.Credentials)
	writers := make([]*snapshotter.Writer, 0, len(cfg.Tables))

	for i, tbl := range cfg.Tables {
		dests, err := buildDestinations(ctx, awsCfg, tbl.Destinations)
		if err != nil {
			return nil, fmt.Errorf("tables[%d] (%s): %w", i, tbl.Source.Table, err)
		}
		sinks, err := buildSinks(ctx, awsCfg, tbl.Events)
		if err != nil {
			return nil, fmt.Errorf("tables[%d] (%s): %w", i, tbl.Source.Table, err)
		}
		snapFmts, err := buildFormats(tbl.SnapshotFormats)
		if err != nil {
			return nil, fmt.Errorf("tables[%d] snapshot formats: %w", i, err)
		}
		diffFmts, err := buildFormats(tbl.DiffFormats)
		if err != nil {
			return nil, fmt.Errorf("tables[%d] diff formats: %w", i, err)
		}

		opts := []fanout.Option{
			fanout.ServerAddress(tbl.Source.Server),
			fanout.Table(tbl.Source.Schema, tbl.Source.Table),
		}
		if tbl.Source.ClientID != "" {
			opts = append(opts, fanout.ClientID(tbl.Source.ClientID))
		}
		if tbl.Source.LocalSnapshotPath != "" {
			opts = append(opts, fanout.LocalSnapshotPath(tbl.Source.LocalSnapshotPath))
		}
		sub := fanoutsub.New(opts...)

		w, err := snapshotter.New(sub, snapshotter.Config{
			Table:           tbl.Source.Schema + "." + tbl.Source.Table,
			KeyPrefix:       tbl.KeyPrefix,
			KeyFields:       tbl.KeyFields,
			KeepEpochs:      tbl.KeepEpochs,
			SnapshotFormats: snapFmts,
			DiffFormats:     diffFmts,
			Destinations:    dests,
			Sinks:           sinks,
			Policy: snapshotter.Policy{
				DiffInterval:     tbl.DiffInterval,
				MinInterval:      tbl.Snapshot.MinInterval,
				MaxInterval:      tbl.Snapshot.MaxInterval,
				MaxDiffBytes:     tbl.Snapshot.MaxDiffBytes,
				MaxDiffFraction:  tbl.Snapshot.MaxDiffFraction,
				MaxChurnRecords:  tbl.Snapshot.MaxChurnRecords,
				MaxChurnFraction: tbl.Snapshot.MaxChurnFraction,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("tables[%d] (%s): %w", i, tbl.Source.Table, err)
		}
		writers = append(writers, w)
	}
	return writers, nil
}

func buildDestinations(ctx context.Context, awsCfg *awsConfigCache, dcs []config.Destination) ([]snapshotter.Destination, error) {
	var out []snapshotter.Destination
	for _, dc := range dcs {
		switch dc.Type {
		case "local":
			out = append(out, localdest.New(dc.Path))
		case "s3":
			cfg, err := awsCfg.get(ctx, dc.Credentials, dc.Region)
			if err != nil {
				return nil, err
			}
			out = append(out, s3dest.New(awss3.NewFromConfig(cfg), dc.Bucket, dc.Prefix))
		default:
			return nil, fmt.Errorf("unknown destination type %q", dc.Type)
		}
	}
	return out, nil
}

func buildSinks(ctx context.Context, awsCfg *awsConfigCache, ecs []config.Event) ([]snapshotter.EventSink, error) {
	var out []snapshotter.EventSink
	for _, ec := range ecs {
		cfg, err := awsCfg.get(ctx, ec.Credentials, ec.Region)
		if err != nil {
			return nil, err
		}
		switch ec.Type {
		case "sns":
			out = append(out, snssink.New(awssns.NewFromConfig(cfg), ec.TopicARN))
		case "sqs":
			out = append(out, sqssink.New(awssqs.NewFromConfig(cfg), ec.QueueURL))
		case "kinesis":
			out = append(out, kinesissink.New(awskinesis.NewFromConfig(cfg), ec.Stream))
		default:
			return nil, fmt.Errorf("unknown event sink type %q", ec.Type)
		}
	}
	return out, nil
}

func buildFormats(ids []string) ([]snapshotter.Format, error) {
	var out []snapshotter.Format
	for _, id := range ids {
		switch id {
		case "jsonl":
			out = append(out, jsonl.New())
		case "protobuf":
			out = append(out, protobuf.New())
		default:
			return nil, fmt.Errorf("unknown format %q", id)
		}
	}
	return out, nil
}
