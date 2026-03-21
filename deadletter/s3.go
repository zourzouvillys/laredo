package deadletter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/zourzouvillys/laredo"
)

// S3Store persists dead letter entries as JSONL files in S3.
// Layout: {prefix}/{pipelineID}.jsonl — one JSON object per line.
type S3Store struct {
	client *s3.Client
	bucket string
	prefix string
}

// S3StoreOption configures the S3 dead letter store.
type S3StoreOption func(*S3Store)

// S3WithClient sets the S3 client.
func S3WithClient(client *s3.Client) S3StoreOption {
	return func(s *S3Store) { s.client = client }
}

// S3WithBucket sets the S3 bucket.
func S3WithBucket(bucket string) S3StoreOption {
	return func(s *S3Store) { s.bucket = bucket }
}

// S3WithPrefix sets the S3 key prefix.
func S3WithPrefix(prefix string) S3StoreOption {
	return func(s *S3Store) { s.prefix = prefix }
}

// NewS3Store creates a new S3-backed dead letter store.
func NewS3Store(opts ...S3StoreOption) *S3Store {
	s := &S3Store{prefix: "dead-letters/"}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ Store = (*S3Store)(nil)

type s3Entry struct {
	PipelineID string    `json:"pipeline_id"`
	Timestamp  time.Time `json:"timestamp"`
	Action     string    `json:"action"`
	Position   string    `json:"position,omitempty"`
	Error      string    `json:"error"`

	NewValues laredo.Row `json:"new_values,omitempty"`
	OldValues laredo.Row `json:"old_values,omitempty"`
}

func (s *S3Store) key(pipelineID string) string {
	return s.prefix + pipelineID + ".jsonl"
}

//nolint:revive // implements Store.
func (s *S3Store) Write(pipelineID string, change laredo.ChangeEvent, errInfo laredo.ErrorInfo) error {
	ctx := context.Background()

	entry := s3Entry{
		PipelineID: pipelineID,
		Timestamp:  change.Timestamp,
		Action:     change.Action.String(),
		Error:      errInfo.Message,
		NewValues:  change.NewValues,
		OldValues:  change.OldValues,
	}
	if change.Position != nil {
		entry.Position = fmt.Sprintf("%v", change.Position)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	data = append(data, '\n')

	// Read existing content (append semantics).
	existing, _ := s.readRaw(ctx, pipelineID)
	combined := append(existing, data...)

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(pipelineID)),
		Body:   bytes.NewReader(combined),
	})
	return err
}

//nolint:revive // implements Store.
func (s *S3Store) Read(pipelineID string, limit int) ([]Entry, error) {
	ctx := context.Background()
	raw, err := s.readRaw(ctx, pipelineID)
	if err != nil || len(raw) == 0 {
		return nil, nil //nolint:nilerr // empty is not an error
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var entries []Entry
	for _, line := range lines {
		if line == "" {
			continue
		}
		var se s3Entry
		if err := json.Unmarshal([]byte(line), &se); err != nil {
			continue
		}
		entries = append(entries, Entry{
			PipelineID: se.PipelineID,
			Change: laredo.ChangeEvent{
				Timestamp: se.Timestamp,
				NewValues: se.NewValues,
				OldValues: se.OldValues,
			},
			Error: laredo.ErrorInfo{Message: se.Error},
		})
		if limit > 0 && len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

//nolint:revive // implements Store.
func (s *S3Store) Replay(pipelineID string, target laredo.SyncTarget) error {
	entries, err := s.Read(pipelineID, 0)
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, e := range entries {
		if err := target.OnInsert(ctx, laredo.TableIdentifier{}, e.Change.NewValues); err != nil {
			return err
		}
	}
	return nil
}

//nolint:revive // implements Store.
func (s *S3Store) Purge(pipelineID string) error {
	ctx := context.Background()
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(pipelineID)),
	})
	return err
}

func (s *S3Store) readRaw(ctx context.Context, pipelineID string) ([]byte, error) {
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(pipelineID)),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}
