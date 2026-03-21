// Package s3 implements a SnapshotStore backed by Amazon S3.
// Layout: {prefix}/{snapshot_id}/metadata.json + {table}.jsonl per table.
package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/zourzouvillys/laredo"
)

// Store implements laredo.SnapshotStore using S3 storage.
type Store struct {
	client *awss3.Client
	bucket string
	prefix string
}

// Option configures the S3 snapshot store.
type Option func(*Store)

// WithClient sets the S3 client (for LocalStack testing).
func WithClient(client *awss3.Client) Option {
	return func(s *Store) { s.client = client }
}

// WithBucket sets the S3 bucket name.
func WithBucket(bucket string) Option {
	return func(s *Store) { s.bucket = bucket }
}

// WithPrefix sets the S3 key prefix.
func WithPrefix(prefix string) Option {
	return func(s *Store) { s.prefix = prefix }
}

// New creates a new S3 snapshot store.
func New(opts ...Option) *Store {
	s := &Store{}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ laredo.SnapshotStore = (*Store)(nil)

// metadata is the JSON structure stored in {snapshot_id}/metadata.json.
type metadata struct {
	SnapshotID string    `json:"snapshot_id"`
	CreatedAt  time.Time `json:"created_at"`
	Format     string    `json:"format"`

	SourcePositions map[string]string          `json:"source_positions,omitempty"`
	Tables          []laredo.TableSnapshotInfo `json:"tables"`
	UserMeta        map[string]laredo.Value    `json:"user_meta,omitempty"`
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Save(ctx context.Context, snapshotID string, meta laredo.SnapshotMetadata, entries map[laredo.TableIdentifier][]laredo.SnapshotEntry) (laredo.SnapshotDescriptor, error) {
	prefix := s.keyPrefix(snapshotID)

	// Write per-table JSONL files.
	var totalSize int64
	for table, tableEntries := range entries {
		var buf bytes.Buffer
		for _, entry := range tableEntries {
			data, err := json.Marshal(entry.Row)
			if err != nil {
				return laredo.SnapshotDescriptor{}, fmt.Errorf("marshal row: %w", err)
			}
			buf.Write(data)
			buf.WriteByte('\n')
		}

		key := prefix + table.Schema + "." + table.Table + ".jsonl"
		_, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
			Body:   bytes.NewReader(buf.Bytes()),
		})
		if err != nil {
			return laredo.SnapshotDescriptor{}, fmt.Errorf("put %s: %w", key, err)
		}
		totalSize += int64(buf.Len())
	}

	// Write metadata.json.
	md := metadata{
		SnapshotID: snapshotID,
		CreatedAt:  meta.CreatedAt,
		Format:     meta.Format,
		Tables:     meta.Tables,
		UserMeta:   meta.UserMeta,
	}
	if meta.SourcePositions != nil {
		md.SourcePositions = make(map[string]string, len(meta.SourcePositions))
		for k, v := range meta.SourcePositions {
			md.SourcePositions[k] = fmt.Sprintf("%v", v)
		}
	}

	mdJSON, err := json.Marshal(md)
	if err != nil {
		return laredo.SnapshotDescriptor{}, fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(prefix + "metadata.json"),
		Body:   bytes.NewReader(mdJSON),
	})
	if err != nil {
		return laredo.SnapshotDescriptor{}, fmt.Errorf("put metadata: %w", err)
	}

	return laredo.SnapshotDescriptor{
		SnapshotID: snapshotID,
		CreatedAt:  meta.CreatedAt,
		Format:     meta.Format,
		Tables:     meta.Tables,
		SizeBytes:  totalSize,
	}, nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Load(ctx context.Context, snapshotID string) (laredo.SnapshotMetadata, map[laredo.TableIdentifier][]laredo.SnapshotEntry, error) {
	prefix := s.keyPrefix(snapshotID)

	// Load metadata.
	md, err := s.loadMetadata(ctx, prefix)
	if err != nil {
		return laredo.SnapshotMetadata{}, nil, err
	}

	meta := laredo.SnapshotMetadata{
		SnapshotID: md.SnapshotID,
		CreatedAt:  md.CreatedAt,
		Format:     md.Format,
		Tables:     md.Tables,
		UserMeta:   md.UserMeta,
	}

	// Load per-table JSONL files.
	entries := make(map[laredo.TableIdentifier][]laredo.SnapshotEntry, len(md.Tables))
	for _, tableInfo := range md.Tables {
		key := prefix + tableInfo.Table.Schema + "." + tableInfo.Table.Table + ".jsonl"
		tableEntries, err := s.loadJSONL(ctx, key)
		if err != nil {
			return laredo.SnapshotMetadata{}, nil, fmt.Errorf("load %s: %w", key, err)
		}
		entries[tableInfo.Table] = tableEntries
	}

	return meta, entries, nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Describe(ctx context.Context, snapshotID string) (laredo.SnapshotDescriptor, error) {
	prefix := s.keyPrefix(snapshotID)
	md, err := s.loadMetadata(ctx, prefix)
	if err != nil {
		return laredo.SnapshotDescriptor{}, err
	}

	return laredo.SnapshotDescriptor{
		SnapshotID: md.SnapshotID,
		CreatedAt:  md.CreatedAt,
		Format:     md.Format,
		Tables:     md.Tables,
	}, nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) List(ctx context.Context, filter *laredo.SnapshotFilter) ([]laredo.SnapshotDescriptor, error) {
	// List all "directories" under the prefix using delimiter.
	listResp, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket:    aws.String(s.bucket),
		Prefix:    aws.String(s.prefix),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	var descriptors []laredo.SnapshotDescriptor
	for _, cp := range listResp.CommonPrefixes {
		// Extract snapshot ID from prefix: "{prefix}{id}/" → "{id}"
		snapPrefix := aws.ToString(cp.Prefix)
		snapID := strings.TrimPrefix(snapPrefix, s.prefix)
		snapID = strings.TrimSuffix(snapID, "/")
		if snapID == "" {
			continue
		}

		md, err := s.loadMetadata(ctx, snapPrefix)
		if err != nil {
			continue // Skip snapshots with missing/corrupt metadata.
		}

		desc := laredo.SnapshotDescriptor{
			SnapshotID: md.SnapshotID,
			CreatedAt:  md.CreatedAt,
			Format:     md.Format,
			Tables:     md.Tables,
		}

		// Apply table filter.
		if filter != nil && filter.Table != nil {
			found := false
			for _, t := range md.Tables {
				if t.Table == *filter.Table {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		descriptors = append(descriptors, desc)
	}

	// Sort newest first.
	sort.Slice(descriptors, func(i, j int) bool {
		return descriptors[i].CreatedAt.After(descriptors[j].CreatedAt)
	})

	return descriptors, nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Delete(ctx context.Context, snapshotID string) error {
	prefix := s.keyPrefix(snapshotID)

	// List all objects under this snapshot.
	listResp, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return fmt.Errorf("list objects for delete: %w", err)
	}

	for _, obj := range listResp.Contents {
		_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    obj.Key,
		})
		if err != nil {
			return fmt.Errorf("delete %s: %w", aws.ToString(obj.Key), err)
		}
	}

	return nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Prune(ctx context.Context, keep int, table *laredo.TableIdentifier) error {
	var filter *laredo.SnapshotFilter
	if table != nil {
		filter = &laredo.SnapshotFilter{Table: table}
	}

	descriptors, err := s.List(ctx, filter)
	if err != nil {
		return err
	}

	// Delete all but the most recent `keep`.
	if len(descriptors) <= keep {
		return nil
	}
	for _, d := range descriptors[keep:] {
		if err := s.Delete(ctx, d.SnapshotID); err != nil {
			return err
		}
	}
	return nil
}

// --- helpers ---

func (s *Store) keyPrefix(snapshotID string) string {
	return s.prefix + snapshotID + "/"
}

func (s *Store) loadMetadata(ctx context.Context, prefix string) (metadata, error) {
	resp, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(prefix + "metadata.json"),
	})
	if err != nil {
		return metadata{}, fmt.Errorf("get metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var md metadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		return metadata{}, fmt.Errorf("decode metadata: %w", err)
	}
	return md, nil
}

func (s *Store) loadJSONL(ctx context.Context, key string) ([]laredo.SnapshotEntry, error) {
	resp, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}

	var entries []laredo.SnapshotEntry
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var row laredo.Row
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("parse row: %w", err)
		}
		entries = append(entries, laredo.SnapshotEntry{Row: row})
	}
	return entries, nil
}
