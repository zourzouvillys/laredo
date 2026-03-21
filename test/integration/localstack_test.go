//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	kinTypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/zourzouvillys/laredo/test/testutil"
)

// --- S3 Tests ---

func TestLocalStack_S3_CreateBucketAndPutObject(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "test-bucket")

	ctx := context.Background()

	// Put an object.
	content := []byte(`{"id": 1, "name": "alice"}`)
	_, err := ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("data/row1.json"),
		Body:   bytes.NewReader(content),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// Get it back.
	getResp, err := ls.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("data/row1.json"),
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer getResp.Body.Close()

	body, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if string(body) != string(content) {
		t.Errorf("expected %q, got %q", content, body)
	}
}

func TestLocalStack_S3_ListObjects(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "snapshot-bucket")

	ctx := context.Background()

	// Put multiple objects with a prefix.
	for i, name := range []string{"snap-001/metadata.json", "snap-001/public.users.jsonl", "snap-002/metadata.json"} {
		_, err := ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("snapshot-bucket"),
			Key:    aws.String(name),
			Body:   bytes.NewReader([]byte(`{}`)),
		})
		if err != nil {
			t.Fatalf("PutObject %d: %v", i, err)
		}
	}

	// List with prefix filter.
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
}

func TestLocalStack_S3_DeleteObject(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "dl-bucket")

	ctx := context.Background()

	_, _ = ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("dl-bucket"),
		Key:    aws.String("dead-letters/pipe-1.jsonl"),
		Body:   bytes.NewReader([]byte("line1\nline2\n")),
	})

	// Delete it.
	_, err := ls.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String("dl-bucket"),
		Key:    aws.String("dead-letters/pipe-1.jsonl"),
	})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}

	// Verify it's gone.
	_, err = ls.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("dl-bucket"),
		Key:    aws.String("dead-letters/pipe-1.jsonl"),
	})
	if err == nil {
		t.Error("expected error getting deleted object")
	}
}

// --- Kinesis Tests ---

func TestLocalStack_Kinesis_CreateStreamAndPutRecord(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateKinesisStream(t, "test-stream", 1)

	ctx := context.Background()

	// Put a record.
	record := map[string]any{"id": 1, "action": "INSERT", "name": "alice"}
	data, _ := json.Marshal(record)
	ls.PutKinesisRecord(t, "test-stream", "pk-1", data)

	// Describe the stream.
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

func TestLocalStack_Kinesis_GetRecords(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateKinesisStream(t, "cdc-stream", 1)

	ctx := context.Background()

	// Put multiple records.
	for i := range 3 {
		record := map[string]any{"id": i + 1, "name": "row"}
		data, _ := json.Marshal(record)
		ls.PutKinesisRecord(t, "cdc-stream", "pk", data)
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

	// Get records.
	getResp, err := ls.KinesisClient.GetRecords(ctx, &kinesis.GetRecordsInput{
		ShardIterator: iterResp.ShardIterator,
		Limit:         aws.Int32(10),
	})
	if err != nil {
		t.Fatalf("GetRecords: %v", err)
	}

	if len(getResp.Records) != 3 {
		t.Errorf("expected 3 records, got %d", len(getResp.Records))
	}

	// Verify first record.
	var firstRecord map[string]any
	if err := json.Unmarshal(getResp.Records[0].Data, &firstRecord); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if firstRecord["id"] != float64(1) {
		t.Errorf("expected id=1, got %v", firstRecord["id"])
	}
}

func TestLocalStack_Kinesis_MultipleShards(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateKinesisStream(t, "multi-shard-stream", 3)

	ctx := context.Background()

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
