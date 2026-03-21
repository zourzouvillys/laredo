//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/zourzouvillys/laredo"
	kin "github.com/zourzouvillys/laredo/source/kinesis"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestKinesisSource_S3Baseline(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ctx := context.Background()

	// Create S3 bucket with baseline data.
	ls.CreateS3Bucket(t, "baseline")
	var jsonl strings.Builder
	for i := range 3 {
		row := map[string]any{"id": float64(i + 1), "name": fmt.Sprintf("user-%d", i+1)}
		data, _ := json.Marshal(row)
		jsonl.Write(data)
		jsonl.WriteByte('\n')
	}
	_, _ = ls.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("baseline"),
		Key:    aws.String("data/public.users.jsonl"),
		Body:   bytes.NewReader([]byte(jsonl.String())),
	})

	src := kin.New(
		kin.BaselineBucket("baseline"),
		kin.BaselinePrefix("data/"),
		kin.WithS3Client(ls.S3Client),
	)

	table := laredo.Table("public", "users")
	_, err := src.Init(ctx, laredo.SourceConfig{Tables: []laredo.TableIdentifier{table}})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var rows []laredo.Row
	pos, err := src.Baseline(ctx, []laredo.TableIdentifier{table}, func(_ laredo.TableIdentifier, row laredo.Row) {
		rows = append(rows, row)
	})
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0]["name"] != "user-1" {
		t.Errorf("expected name=user-1, got %v", rows[0]["name"])
	}
	if pos == nil {
		t.Error("expected non-nil position")
	}

	t.Logf("baseline: %d rows, position: %s", len(rows), src.PositionToString(pos))
}

func TestKinesisSource_StreamFromKinesis(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ctx := context.Background()

	// Create Kinesis stream.
	ls.CreateKinesisStream(t, "cdc-stream", 1)

	// Put CDC records.
	for i := range 3 {
		record := map[string]any{
			"table":  "public.users",
			"action": "INSERT",
			"row":    map[string]any{"id": float64(i + 1), "name": fmt.Sprintf("user-%d", i+1)},
		}
		data, _ := json.Marshal(record)
		ls.PutKinesisRecord(t, "cdc-stream", "pk", data)
	}

	src := kin.New(
		kin.StreamName("cdc-stream"),
		kin.WithKinesisClient(ls.KinesisClient),
	)

	_, _ = src.Init(ctx, laredo.SourceConfig{})

	// Stream with a timeout context.
	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var events []laredo.ChangeEvent
	err := src.Stream(streamCtx, nil, laredo.ChangeHandlerFunc(func(evt laredo.ChangeEvent) error {
		events = append(events, evt)
		if len(events) >= 3 {
			cancel() // Got all records, stop streaming.
		}
		return nil
	}))

	// Context cancellation is expected.
	if err != nil && ctx.Err() == nil {
		t.Logf("Stream error (may be expected): %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	for i, evt := range events {
		if evt.Action != laredo.ActionInsert {
			t.Errorf("event %d: expected INSERT, got %v", i, evt.Action)
		}
		if evt.Table.Schema != "public" || evt.Table.Table != "users" {
			t.Errorf("event %d: unexpected table %v", i, evt.Table)
		}
	}

	t.Logf("streamed %d events", len(events))
}

func TestKinesisSource_PositionRoundTrip(t *testing.T) {
	src := kin.New()

	pos := &kin.Position{
		S3Version:      "v1",
		ShardSequences: map[string]string{"shard-0": "seq-123"},
	}

	str := src.PositionToString(pos)
	if str == "" {
		t.Fatal("expected non-empty position string")
	}

	parsed, err := src.PositionFromString(str)
	if err != nil {
		t.Fatalf("PositionFromString: %v", err)
	}

	parsedPos := parsed.(*kin.Position)
	if parsedPos.S3Version != "v1" {
		t.Errorf("expected S3Version=v1, got %s", parsedPos.S3Version)
	}
	if parsedPos.ShardSequences["shard-0"] != "seq-123" {
		t.Errorf("expected shard-0=seq-123, got %s", parsedPos.ShardSequences["shard-0"])
	}
}
