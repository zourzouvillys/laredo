// Package destwire builds snapshotter destinations and formats from a neutral,
// config-format-agnostic spec, so every binary that turns configuration into
// object storage — laredo-snapshotter (write side) and laredo-server (read side,
// for cold-tier archives) — wires destinations and resolves credentials through
// one code path. See EDR-0005.
package destwire

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/awsx"
	localdest "github.com/zourzouvillys/laredo/snapshotter/dest/local"
	s3dest "github.com/zourzouvillys/laredo/snapshotter/dest/s3"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
	"github.com/zourzouvillys/laredo/snapshotter/format/protobuf"
)

// DestinationSpec describes one artifact destination, neutral to any particular
// config format. Callers map their own config structs onto it.
type DestinationSpec struct {
	// Type is the backend: "local" or "s3".
	Type string
	// Path is the filesystem root for a local destination.
	Path string
	// Bucket, Prefix, Region configure an s3 destination. Prefix is the
	// storage-level object prefix (distinct from any per-table key prefix).
	Bucket string
	Prefix string
	Region string
	// Credentials names a credentials profile resolved by the AWSConfigFunc the
	// caller supplies. Empty means the resolver's default (typically ambient).
	Credentials string
}

// AWSConfigFunc resolves an aws.Config for an s3 destination, given a credentials
// profile name and a region. laredo-snapshotter supplies a profile-map-aware
// cache; laredo-server supplies AmbientAWSConfig. It is only called for s3.
type AWSConfigFunc func(ctx context.Context, profile, region string) (aws.Config, error)

// AmbientAWSConfig resolves credentials from the AWS SDK default chain
// (environment, web identity / IRSA, EC2/ECS instance or task roles), honouring
// region. It ignores the profile name — callers that need named profiles or
// assume-role supply their own AWSConfigFunc.
func AmbientAWSConfig(ctx context.Context, _, region string) (aws.Config, error) {
	return awsx.Config(ctx, awsx.Profile{Type: "ambient", Region: region})
}

// BuildDestination constructs a snapshotter.Destination from a spec. An s3
// destination requires a non-nil awsConfig resolver (local ignores it).
func BuildDestination(ctx context.Context, spec DestinationSpec, awsConfig AWSConfigFunc) (snapshotter.Destination, error) {
	switch spec.Type {
	case "local":
		if spec.Path == "" {
			return nil, errors.New("local destination requires a path")
		}
		return localdest.New(spec.Path), nil
	case "s3":
		if spec.Bucket == "" {
			return nil, errors.New("s3 destination requires a bucket")
		}
		if awsConfig == nil {
			return nil, errors.New("s3 destination requires an AWS config resolver")
		}
		cfg, err := awsConfig(ctx, spec.Credentials, spec.Region)
		if err != nil {
			return nil, err
		}
		return s3dest.New(awss3.NewFromConfig(cfg), spec.Bucket, spec.Prefix), nil
	case "":
		return nil, errors.New("destination type is required (local or s3)")
	default:
		return nil, fmt.Errorf("unknown destination type %q (want local or s3)", spec.Type)
	}
}

// BuildFormats maps artifact format ids to codecs, in order. Recognised ids are
// "jsonl" and "protobuf".
func BuildFormats(ids []string) ([]snapshotter.Format, error) {
	out := make([]snapshotter.Format, 0, len(ids))
	for _, id := range ids {
		switch id {
		case "jsonl":
			out = append(out, jsonl.New())
		case "protobuf":
			out = append(out, protobuf.New())
		default:
			return nil, fmt.Errorf("unknown format %q (want jsonl or protobuf)", id)
		}
	}
	return out, nil
}
