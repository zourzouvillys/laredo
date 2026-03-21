//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/deadletter"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestS3DeadLetterStore_WriteAndRead(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "dl-bucket")

	store := deadletter.NewS3Store(
		deadletter.S3WithClient(ls.S3Client),
		deadletter.S3WithBucket("dl-bucket"),
	)

	// Write entries.
	for i := range 3 {
		evt := laredo.ChangeEvent{
			Table:     laredo.Table("public", "users"),
			Action:    laredo.ActionInsert,
			Timestamp: time.Now(),
			NewValues: laredo.Row{"id": float64(i + 1)},
		}
		if err := store.Write("pipe-1", evt, laredo.ErrorInfo{Message: "test error"}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	// Read all.
	entries, err := store.Read("pipe-1", 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Error.Message != "test error" {
		t.Errorf("expected 'test error', got %q", entries[0].Error.Message)
	}

	// Read with limit.
	limited, _ := store.Read("pipe-1", 2)
	if len(limited) != 2 {
		t.Errorf("expected 2 with limit, got %d", len(limited))
	}
}

func TestS3DeadLetterStore_Purge(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "dl-purge")

	store := deadletter.NewS3Store(
		deadletter.S3WithClient(ls.S3Client),
		deadletter.S3WithBucket("dl-purge"),
	)

	evt := laredo.ChangeEvent{Action: laredo.ActionInsert, Timestamp: time.Now()}
	_ = store.Write("pipe-1", evt, laredo.ErrorInfo{Message: "err"})

	// Purge.
	if err := store.Purge("pipe-1"); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	// Verify empty.
	entries, _ := store.Read("pipe-1", 0)
	if len(entries) != 0 {
		t.Errorf("expected 0 after purge, got %d", len(entries))
	}
}

func TestS3DeadLetterStore_ReadEmpty(t *testing.T) {
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "dl-empty")

	store := deadletter.NewS3Store(
		deadletter.S3WithClient(ls.S3Client),
		deadletter.S3WithBucket("dl-empty"),
	)

	entries, err := store.Read("nonexistent", 0)
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}
