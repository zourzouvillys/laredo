//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	kinTypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/zourzouvillys/laredo/test/testutil"
)

// --- S3 Tests ---

func TestS3_BucketLifecycle(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ctx := context.Background()

	// Create bucket.
	ls.CreateS3Bucket(t, "test-bucket")

	// List buckets.
	listResp, err := ls.S3Client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	found := false
	for _, b := range listResp.Buckets {
		if aws.ToString(b.Name) == "test-bucket" {
			found = true
		}
	}
	if !found {
		t.Error("expected test-bucket in bucket list")
	}
}

func TestS3_ObjectCRUD(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "crud-bucket")
	ctx := context.Background()

	// Put.
	content := []byte(`{"id": 1, "name": "alice"}`)
	_, err := ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("crud-bucket"),
		Key:    aws.String("data/row1.json"),
		Body:   bytes.NewReader(content),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// Get.
	getResp, err := ls.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("crud-bucket"),
		Key:    aws.String("data/row1.json"),
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer getResp.Body.Close()
	body, _ := io.ReadAll(getResp.Body)
	if string(body) != string(content) {
		t.Errorf("expected %q, got %q", content, body)
	}

	// Delete.
	_, err = ls.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String("crud-bucket"),
		Key:    aws.String("data/row1.json"),
	})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}

	// Verify deleted.
	_, err = ls.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("crud-bucket"),
		Key:    aws.String("data/row1.json"),
	})
	if err == nil {
		t.Error("expected error getting deleted object")
	}
}

func TestS3_ListWithPrefix(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "snapshot-bucket")
	ctx := context.Background()

	// Simulate snapshot store layout: {snapshot_id}/{table}.jsonl
	keys := []string{
		"snap-001/metadata.json",
		"snap-001/public.users.jsonl",
		"snap-002/metadata.json",
		"snap-002/public.users.jsonl",
		"snap-003/metadata.json",
	}
	for _, key := range keys {
		_, err := ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("snapshot-bucket"),
			Key:    aws.String(key),
			Body:   bytes.NewReader([]byte(`{}`)),
		})
		if err != nil {
			t.Fatalf("PutObject %s: %v", key, err)
		}
	}

	// List with prefix filter for snap-001.
	listResp, err := ls.S3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("snapshot-bucket"),
		Prefix: aws.String("snap-001/"),
	})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(listResp.Contents) != 2 {
		t.Errorf("expected 2 objects under snap-001/, got %d", len(listResp.Contents))
	}

	// List all — should find 5 objects.
	listAll, err := ls.S3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("snapshot-bucket"),
	})
	if err != nil {
		t.Fatalf("ListObjectsV2 all: %v", err)
	}
	if len(listAll.Contents) != 5 {
		t.Errorf("expected 5 total objects, got %d", len(listAll.Contents))
	}

	// List with delimiter to get snapshot "directories".
	listDirs, err := ls.S3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String("snapshot-bucket"),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		t.Fatalf("ListObjectsV2 dirs: %v", err)
	}
	if len(listDirs.CommonPrefixes) != 3 {
		t.Errorf("expected 3 snapshot prefixes, got %d", len(listDirs.CommonPrefixes))
	}
}

func TestS3_SnapshotStorePattern(t *testing.T) {
	// Simulates the S3 snapshot store pattern: save metadata + JSONL data,
	// then list and load back.
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "laredo-snapshots")
	ctx := context.Background()

	snapshotID := "snap-20260321-001"
	prefix := snapshotID + "/"

	// Save metadata.
	metadata := map[string]any{
		"snapshot_id": snapshotID,
		"created_at":  time.Now().Format(time.RFC3339),
		"tables":      []string{"public.users"},
		"row_count":   3,
	}
	metaJSON, _ := json.Marshal(metadata)
	_, _ = ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("laredo-snapshots"),
		Key:    aws.String(prefix + "metadata.json"),
		Body:   bytes.NewReader(metaJSON),
	})

	// Save JSONL data.
	var jsonl strings.Builder
	for i := range 3 {
		row := map[string]any{"id": i + 1, "name": fmt.Sprintf("user-%d", i+1)}
		data, _ := json.Marshal(row)
		jsonl.Write(data)
		jsonl.WriteByte('\n')
	}
	_, _ = ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("laredo-snapshots"),
		Key:    aws.String(prefix + "public.users.jsonl"),
		Body:   bytes.NewReader([]byte(jsonl.String())),
	})

	// Load metadata back.
	getResp, err := ls.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("laredo-snapshots"),
		Key:    aws.String(prefix + "metadata.json"),
	})
	if err != nil {
		t.Fatalf("GetObject metadata: %v", err)
	}
	defer getResp.Body.Close()
	var loadedMeta map[string]any
	if err := json.NewDecoder(getResp.Body).Decode(&loadedMeta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if loadedMeta["snapshot_id"] != snapshotID {
		t.Errorf("expected snapshot_id=%s, got %v", snapshotID, loadedMeta["snapshot_id"])
	}

	// Load JSONL data back.
	dataResp, err := ls.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("laredo-snapshots"),
		Key:    aws.String(prefix + "public.users.jsonl"),
		})
	if err != nil {
		t.Fatalf("GetObject data: %v", err)
	}
	defer dataResp.Body.Close()
	dataBody, _ := io.ReadAll(dataResp.Body)
	lines := strings.Split(strings.TrimSpace(string(dataBody)), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 JSONL lines, got %d", len(lines))
	}
}

func TestS3_DeadLetterPattern(t *testing.T) {
	// Simulates the S3 dead letter store: append JSONL entries per pipeline.
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "laredo-deadletters")
	ctx := context.Background()

	// Write dead letter entries.
	var entries strings.Builder
	for i := range 3 {
		entry := map[string]any{
			"pipeline_id": "pipe-1",
			"action":      "INSERT",
			"error":       fmt.Sprintf("error-%d", i+1),
			"timestamp":   time.Now().Format(time.RFC3339),
		}
		data, _ := json.Marshal(entry)
		entries.Write(data)
		entries.WriteByte('\n')
	}

	_, err := ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("laredo-deadletters"),
		Key:    aws.String("dead-letters/pipe-1.jsonl"),
		Body:   bytes.NewReader([]byte(entries.String())),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// Read back and count entries.
	getResp, err := ls.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("laredo-deadletters"),
		Key:    aws.String("dead-letters/pipe-1.jsonl"),
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer getResp.Body.Close()
	body, _ := io.ReadAll(getResp.Body)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 dead letter entries, got %d", len(lines))
	}

	// Purge by deleting the file.
	_, err = ls.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String("laredo-deadletters"),
		Key:    aws.String("dead-letters/pipe-1.jsonl"),
	})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
}

// --- Kinesis Tests ---

func TestKinesis_StreamLifecycle(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ctx := context.Background()

	ls.CreateKinesisStream(t, "test-stream", 1)

	// Describe.
	desc, err := ls.KinesisClient.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName: aws.String("test-stream"),
	})
	if err != nil {
		t.Fatalf("DescribeStream: %v", err)
	}
	if desc.StreamDescription.StreamStatus != kinTypes.StreamStatusActive {
		t.Errorf("expected ACTIVE, got %v", desc.StreamDescription.StreamStatus)
	}
	if len(desc.StreamDescription.Shards) != 1 {
		t.Errorf("expected 1 shard, got %d", len(desc.StreamDescription.Shards))
	}
}

func TestKinesis_PutAndGetRecords(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ctx := context.Background()

	ls.CreateKinesisStream(t, "cdc-stream", 1)

	// Put multiple CDC records.
	for i := range 5 {
		record := map[string]any{
			"table":  "public.users",
			"action": "INSERT",
			"row":    map[string]any{"id": i + 1, "name": fmt.Sprintf("user-%d", i+1)},
		}
		data, _ := json.Marshal(record)
		ls.PutKinesisRecord(t, "cdc-stream", fmt.Sprintf("pk-%d", i+1), data)
	}

	// Get shard iterator.
	desc, _ := ls.KinesisClient.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName: aws.String("cdc-stream"),
	})
	shardID := desc.StreamDescription.Shards[0].ShardId

	iterResp, err := ls.KinesisClient.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName:        aws.String("cdc-stream"),
		ShardId:           shardID,
		ShardIteratorType: kinTypes.ShardIteratorTypeTrimHorizon,
	})
	if err != nil {
		t.Fatalf("GetShardIterator: %v", err)
	}

	// Get all records.
	getResp, err := ls.KinesisClient.GetRecords(ctx, &kinesis.GetRecordsInput{
		ShardIterator: iterResp.ShardIterator,
		Limit:         aws.Int32(10),
	})
	if err != nil {
		t.Fatalf("GetRecords: %v", err)
	}
	if len(getResp.Records) != 5 {
		t.Errorf("expected 5 records, got %d", len(getResp.Records))
	}

	// Verify first record structure.
	var firstRecord map[string]any
	if err := json.Unmarshal(getResp.Records[0].Data, &firstRecord); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if firstRecord["action"] != "INSERT" {
		t.Errorf("expected INSERT, got %v", firstRecord["action"])
	}
}

func TestKinesis_MultipleShards(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ctx := context.Background()

	ls.CreateKinesisStream(t, "multi-shard-stream", 3)

	desc, err := ls.KinesisClient.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName: aws.String("multi-shard-stream"),
	})
	if err != nil {
		t.Fatalf("DescribeStream: %v", err)
	}
	if len(desc.StreamDescription.Shards) != 3 {
		t.Errorf("expected 3 shards, got %d", len(desc.StreamDescription.Shards))
	}
}

func TestKinesis_CDCPattern(t *testing.T) {
	// Simulates the S3+Kinesis source pattern: baseline from S3, then
	// stream changes from Kinesis.
	ls := testutil.NewTestLocalStack(t)
	ctx := context.Background()

	// 1. Create S3 baseline data.
	ls.CreateS3Bucket(t, "baseline-bucket")
	var baselineJSONL strings.Builder
	for i := range 3 {
		row := map[string]any{"id": i + 1, "name": fmt.Sprintf("baseline-user-%d", i+1)}
		data, _ := json.Marshal(row)
		baselineJSONL.Write(data)
		baselineJSONL.WriteByte('\n')
	}
	_, _ = ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("baseline-bucket"),
		Key:    aws.String("snapshots/users/data.jsonl"),
		Body:   bytes.NewReader([]byte(baselineJSONL.String())),
	})

	// 2. Create Kinesis stream for CDC.
	ls.CreateKinesisStream(t, "users-cdc", 1)

	// 3. Put CDC events (changes after baseline).
	cdcEvents := []map[string]any{
		{"action": "INSERT", "row": map[string]any{"id": 4, "name": "new-user-4"}},
		{"action": "UPDATE", "row": map[string]any{"id": 1, "name": "updated-user-1"}},
		{"action": "DELETE", "identity": map[string]any{"id": 2}},
	}
	for _, evt := range cdcEvents {
		data, _ := json.Marshal(evt)
		ls.PutKinesisRecord(t, "users-cdc", "users", data)
	}

	// 4. Read baseline from S3.
	baselineResp, err := ls.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("baseline-bucket"),
		Key:    aws.String("snapshots/users/data.jsonl"),
	})
	if err != nil {
		t.Fatalf("GetObject baseline: %v", err)
	}
	defer baselineResp.Body.Close()
	baselineBody, _ := io.ReadAll(baselineResp.Body)
	baselineRows := strings.Split(strings.TrimSpace(string(baselineBody)), "\n")
	if len(baselineRows) != 3 {
		t.Fatalf("expected 3 baseline rows, got %d", len(baselineRows))
	}

	// 5. Read CDC from Kinesis.
	desc, _ := ls.KinesisClient.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName: aws.String("users-cdc"),
	})
	iterResp, _ := ls.KinesisClient.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName:        aws.String("users-cdc"),
		ShardId:           desc.StreamDescription.Shards[0].ShardId,
		ShardIteratorType: kinTypes.ShardIteratorTypeTrimHorizon,
	})
	getResp, err := ls.KinesisClient.GetRecords(ctx, &kinesis.GetRecordsInput{
		ShardIterator: iterResp.ShardIterator,
		Limit:         aws.Int32(10),
	})
	if err != nil {
		t.Fatalf("GetRecords: %v", err)
	}
	if len(getResp.Records) != 3 {
		t.Errorf("expected 3 CDC records, got %d", len(getResp.Records))
	}

	// Verify the combined state: 3 baseline + 1 insert - 1 delete = 3 rows,
	// with user-1 updated.
	state := make(map[int]map[string]any)
	for _, line := range baselineRows {
		var row map[string]any
		json.Unmarshal([]byte(line), &row)
		state[int(row["id"].(float64))] = row
	}
	for _, rec := range getResp.Records {
		var evt map[string]any
		json.Unmarshal(rec.Data, &evt)
		switch evt["action"] {
		case "INSERT":
			row := evt["row"].(map[string]any)
			state[int(row["id"].(float64))] = row
		case "UPDATE":
			row := evt["row"].(map[string]any)
			state[int(row["id"].(float64))] = row
		case "DELETE":
			id := evt["identity"].(map[string]any)
			delete(state, int(id["id"].(float64)))
		}
	}

	if len(state) != 3 {
		t.Errorf("expected 3 rows after CDC, got %d", len(state))
	}
	if state[1]["name"] != "updated-user-1" {
		t.Errorf("expected user-1 to be updated, got %v", state[1]["name"])
	}
	if _, exists := state[2]; exists {
		t.Error("expected user-2 to be deleted")
	}
	if state[4]["name"] != "new-user-4" {
		t.Errorf("expected user-4 to be inserted, got %v", state[4])
	}
}
