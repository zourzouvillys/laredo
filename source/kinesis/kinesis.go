// Package kinesis implements a SyncSource backed by S3 baseline snapshots
// and Kinesis change streams.
package kinesis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	kinTypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/zourzouvillys/laredo"
)

// Position is a composite position tracking S3 baseline version and
// per-shard Kinesis sequence numbers.
type Position struct {
	S3Version      string            `json:"s3_version,omitempty"`
	ShardSequences map[string]string `json:"shard_sequences,omitempty"` // shard ID → sequence number
}

// Source implements laredo.SyncSource using S3 for baseline and Kinesis for streaming.
type Source struct {
	cfg   sourceConfig
	mu    sync.Mutex
	state laredo.SourceState
}

type sourceConfig struct {
	baselineBucket string
	baselinePrefix string
	manifestKey    string
	streamName     string
	region         string
	s3Client       *s3.Client
	kinesisClient  *kinesis.Client
}

// Option configures the Kinesis source.
type Option func(*sourceConfig)

// BaselineBucket sets the S3 bucket for baseline data.
func BaselineBucket(bucket string) Option {
	return func(c *sourceConfig) { c.baselineBucket = bucket }
}

// BaselinePrefix sets the S3 key prefix for baseline objects.
func BaselinePrefix(prefix string) Option {
	return func(c *sourceConfig) { c.baselinePrefix = prefix }
}

// ManifestKey sets the S3 key for the schema manifest file.
// The manifest is a JSON file describing column definitions for each table.
// If not set, Init returns empty schemas and the first baseline row defines
// the schema implicitly.
func ManifestKey(key string) Option {
	return func(c *sourceConfig) { c.manifestKey = key }
}

// StreamName sets the Kinesis stream name.
func StreamName(name string) Option {
	return func(c *sourceConfig) { c.streamName = name }
}

// Region sets the AWS region.
func Region(region string) Option {
	return func(c *sourceConfig) { c.region = region }
}

// WithS3Client sets a pre-configured S3 client (for LocalStack testing).
func WithS3Client(client *s3.Client) Option {
	return func(c *sourceConfig) { c.s3Client = client }
}

// WithKinesisClient sets a pre-configured Kinesis client (for LocalStack testing).
func WithKinesisClient(client *kinesis.Client) Option {
	return func(c *sourceConfig) { c.kinesisClient = client }
}

// New creates a new S3+Kinesis source.
func New(opts ...Option) *Source {
	cfg := sourceConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Source{cfg: cfg, state: laredo.SourceClosed}
}

var _ laredo.SyncSource = (*Source)(nil)

// Manifest is the on-disk format for the S3 schema manifest file.
type Manifest struct {
	Tables map[string]ManifestTable `json:"tables"` // "schema.table" → definition
}

// ManifestTable describes a single table in the manifest.
type ManifestTable struct {
	Columns []ManifestColumn `json:"columns"`
}

// ManifestColumn describes a single column in the manifest.
type ManifestColumn struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	PrimaryKey bool   `json:"primary_key"`
}

//nolint:revive // implements SyncSource.
func (s *Source) Init(ctx context.Context, _ laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	s.setState(laredo.SourceConnected)

	schemas := make(map[laredo.TableIdentifier][]laredo.ColumnDefinition)

	// Load schema manifest from S3 if configured.
	if s.cfg.manifestKey != "" && s.cfg.s3Client != nil {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return nil, fmt.Errorf("load manifest: %w", err)
		}
		for tableKey, mt := range manifest.Tables {
			schema, table, ok := strings.Cut(tableKey, ".")
			if !ok {
				continue
			}
			tid := laredo.Table(schema, table)
			cols := make([]laredo.ColumnDefinition, len(mt.Columns))
			for i, mc := range mt.Columns {
				cols[i] = laredo.ColumnDefinition{
					Name:       mc.Name,
					Type:       mc.Type,
					Nullable:   mc.Nullable,
					PrimaryKey: mc.PrimaryKey,
				}
			}
			schemas[tid] = cols
		}
	}

	return schemas, nil
}

func (s *Source) loadManifest(ctx context.Context) (*Manifest, error) {
	resp, err := s.cfg.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.baselineBucket),
		Key:    aws.String(s.cfg.manifestKey),
	})
	if err != nil {
		return nil, fmt.Errorf("get manifest from s3://%s/%s: %w", s.cfg.baselineBucket, s.cfg.manifestKey, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var manifest Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &manifest, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) ValidateTables(_ context.Context, _ []laredo.TableIdentifier) []laredo.ValidationError {
	return nil
}

// Baseline reads JSONL data from S3 for each table and delivers rows via callback.
//
//nolint:revive // implements SyncSource.
func (s *Source) Baseline(ctx context.Context, tables []laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	if s.cfg.s3Client == nil {
		return nil, fmt.Errorf("kinesis source: S3 client not configured")
	}

	pos := &Position{ShardSequences: make(map[string]string)}

	for _, table := range tables {
		key := s.cfg.baselinePrefix + table.Schema + "." + table.Table + ".jsonl"

		resp, err := s.cfg.s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.cfg.baselineBucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return nil, fmt.Errorf("get baseline for %s: %w", table, err)
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read baseline for %s: %w", table, err)
		}

		if resp.VersionId != nil {
			pos.S3Version = *resp.VersionId
		}

		// Parse JSONL.
		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			var row laredo.Row
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				return nil, fmt.Errorf("parse baseline row for %s: %w", table, err)
			}
			rowCallback(table, row)
		}
	}

	return pos, nil
}

// Stream reads change events from Kinesis shards and delivers them via handler.
//
//nolint:revive // implements SyncSource.
func (s *Source) Stream(ctx context.Context, from laredo.Position, handler laredo.ChangeHandler) error {
	if s.cfg.kinesisClient == nil {
		return fmt.Errorf("kinesis source: Kinesis client not configured")
	}

	s.setState(laredo.SourceStreaming)

	// Describe stream to get shard list.
	desc, err := s.cfg.kinesisClient.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName: aws.String(s.cfg.streamName),
	})
	if err != nil {
		return fmt.Errorf("describe stream: %w", err)
	}

	// Get starting position from the Position if available.
	var pos *Position
	if from != nil {
		if p, ok := from.(*Position); ok {
			pos = p
		}
	}

	// Start a consumer for each shard.
	var wg sync.WaitGroup
	errCh := make(chan error, len(desc.StreamDescription.Shards))

	for _, shard := range desc.StreamDescription.Shards {
		wg.Add(1)
		go func(shardID string) {
			defer wg.Done()
			if err := s.consumeShard(ctx, shardID, pos, handler); err != nil && ctx.Err() == nil {
				errCh <- err
			}
		}(aws.ToString(shard.ShardId))
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		return err
	}
	return ctx.Err()
}

func (s *Source) consumeShard(ctx context.Context, shardID string, pos *Position, handler laredo.ChangeHandler) error {
	// Determine iterator type based on position.
	iterType := kinTypes.ShardIteratorTypeTrimHorizon
	var startSeq *string
	if pos != nil {
		if seq, ok := pos.ShardSequences[shardID]; ok {
			iterType = kinTypes.ShardIteratorTypeAfterSequenceNumber
			startSeq = aws.String(seq)
		}
	}

	iterResp, err := s.cfg.kinesisClient.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName:             aws.String(s.cfg.streamName),
		ShardId:                aws.String(shardID),
		ShardIteratorType:      iterType,
		StartingSequenceNumber: startSeq,
	})
	if err != nil {
		return fmt.Errorf("get shard iterator for %s: %w", shardID, err)
	}

	iterator := iterResp.ShardIterator
	for iterator != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		getResp, err := s.cfg.kinesisClient.GetRecords(ctx, &kinesis.GetRecordsInput{
			ShardIterator: iterator,
			Limit:         aws.Int32(100),
		})
		if err != nil {
			return fmt.Errorf("get records from %s: %w", shardID, err)
		}

		for _, record := range getResp.Records {
			var evt struct {
				Table    string     `json:"table"`
				Action   string     `json:"action"`
				Row      laredo.Row `json:"row"`
				Identity laredo.Row `json:"identity"`
			}
			if err := json.Unmarshal(record.Data, &evt); err != nil {
				continue // Skip unparseable records.
			}

			// Parse table identifier.
			schema, table, _ := strings.Cut(evt.Table, ".")
			if schema == "" {
				continue
			}

			change := laredo.ChangeEvent{
				Table:     laredo.Table(schema, table),
				Position:  &Position{ShardSequences: map[string]string{shardID: aws.ToString(record.SequenceNumber)}},
				Timestamp: time.Now(),
			}

			switch evt.Action {
			case "INSERT":
				change.Action = laredo.ActionInsert
				change.NewValues = evt.Row
			case "UPDATE":
				change.Action = laredo.ActionUpdate
				change.NewValues = evt.Row
				change.OldValues = evt.Identity
			case "DELETE":
				change.Action = laredo.ActionDelete
				change.OldValues = evt.Identity
			default:
				continue
			}

			if err := handler.OnChange(change); err != nil {
				return err
			}
		}

		iterator = getResp.NextShardIterator
		if len(getResp.Records) == 0 {
			// No records — back off to avoid throttling.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}
	}

	return nil // Shard closed (split/merge).
}

//nolint:revive // implements SyncSource.
func (s *Source) Ack(_ context.Context, _ laredo.Position) error {
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) SupportsResume() bool { return false }

//nolint:revive // implements SyncSource.
func (s *Source) LastAckedPosition(_ context.Context) (laredo.Position, error) {
	return nil, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) ComparePositions(_, _ laredo.Position) int { return 0 }

//nolint:revive // implements SyncSource.
func (s *Source) PositionToString(p laredo.Position) string {
	if pos, ok := p.(*Position); ok {
		data, _ := json.Marshal(pos)
		return string(data)
	}
	return ""
}

//nolint:revive // implements SyncSource.
func (s *Source) PositionFromString(str string) (laredo.Position, error) {
	var pos Position
	if err := json.Unmarshal([]byte(str), &pos); err != nil {
		return nil, err
	}
	return &pos, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) Pause(_ context.Context) error {
	s.setState(laredo.SourcePaused)
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) Resume(_ context.Context) error {
	s.setState(laredo.SourceStreaming)
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) GetLag() laredo.LagInfo { return laredo.LagInfo{} }

//nolint:revive // implements SyncSource.
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return laredo.PerPartitionOrder }

//nolint:revive // implements SyncSource.
func (s *Source) State() laredo.SourceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

//nolint:revive // implements SyncSource.
func (s *Source) Close(_ context.Context) error {
	s.setState(laredo.SourceClosed)
	return nil
}

func (s *Source) setState(state laredo.SourceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}
