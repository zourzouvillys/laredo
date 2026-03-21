//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	s3store "github.com/zourzouvillys/laredo/snapshot/s3"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func newS3Store(t *testing.T) (*s3store.Store, *testutil.LocalStackContainer) {
	t.Helper()
	ls := testutil.NewTestLocalStack(t)
	ls.CreateS3Bucket(t, "snapshots")

	store := s3store.New(
		s3store.WithClient(ls.S3Client),
		s3store.WithBucket("snapshots"),
		s3store.WithPrefix("laredo/"),
	)
	return store, ls
}

func TestS3SnapshotStore_SaveAndLoad(t *testing.T) {
	store, _ := newS3Store(t)
	ctx := context.Background()
	table := laredo.Table("public", "users")

	meta := laredo.SnapshotMetadata{
		SnapshotID: "snap-001",
		CreatedAt:  time.Now().Truncate(time.Second),
		Format:     "jsonl",
		Tables: []laredo.TableSnapshotInfo{
			{Table: table, RowCount: 2},
		},
	}
	entries := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		table: {
			{Row: laredo.Row{"id": float64(1), "name": "alice"}},
			{Row: laredo.Row{"id": float64(2), "name": "bob"}},
		},
	}

	desc, err := store.Save(ctx, "snap-001", meta, entries)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if desc.SnapshotID != "snap-001" {
		t.Errorf("expected snap-001, got %s", desc.SnapshotID)
	}

	// Load it back.
	loadedMeta, loadedEntries, err := store.Load(ctx, "snap-001")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loadedMeta.SnapshotID != "snap-001" {
		t.Errorf("expected snap-001, got %s", loadedMeta.SnapshotID)
	}

	tableEntries := loadedEntries[table]
	if len(tableEntries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(tableEntries))
	}
	if tableEntries[0].Row["name"] != "alice" {
		t.Errorf("expected alice, got %v", tableEntries[0].Row["name"])
	}
}

func TestS3SnapshotStore_Describe(t *testing.T) {
	store, _ := newS3Store(t)
	ctx := context.Background()
	table := laredo.Table("public", "users")

	meta := laredo.SnapshotMetadata{
		SnapshotID: "snap-desc",
		CreatedAt:  time.Now().Truncate(time.Second),
		Format:     "jsonl",
		Tables:     []laredo.TableSnapshotInfo{{Table: table, RowCount: 1}},
	}
	entries := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		table: {{Row: laredo.Row{"id": float64(1)}}},
	}

	_, _ = store.Save(ctx, "snap-desc", meta, entries)

	desc, err := store.Describe(ctx, "snap-desc")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.SnapshotID != "snap-desc" {
		t.Errorf("expected snap-desc, got %s", desc.SnapshotID)
	}
	if desc.Format != "jsonl" {
		t.Errorf("expected jsonl, got %s", desc.Format)
	}
}

func TestS3SnapshotStore_ListAndDelete(t *testing.T) {
	store, _ := newS3Store(t)
	ctx := context.Background()
	table := laredo.Table("public", "users")

	// Save 3 snapshots.
	for i := range 3 {
		id := laredo.Row{"id": float64(i)}
		meta := laredo.SnapshotMetadata{
			SnapshotID: fmt.Sprintf("snap-%03d", i+1),
			CreatedAt:  time.Now().Add(time.Duration(i) * time.Second).Truncate(time.Second),
			Tables:     []laredo.TableSnapshotInfo{{Table: table, RowCount: 1}},
		}
		_, _ = store.Save(ctx, meta.SnapshotID, meta, map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			table: {{Row: id}},
		})
	}

	// List all.
	descriptors, err := store.List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(descriptors) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(descriptors))
	}

	// Delete one.
	if err := store.Delete(ctx, "snap-002"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// List again.
	descriptors, _ = store.List(ctx, nil)
	if len(descriptors) != 2 {
		t.Errorf("expected 2 snapshots after delete, got %d", len(descriptors))
	}
}

func TestS3SnapshotStore_Prune(t *testing.T) {
	store, _ := newS3Store(t)
	ctx := context.Background()
	table := laredo.Table("public", "users")

	for i := range 5 {
		meta := laredo.SnapshotMetadata{
			SnapshotID: fmt.Sprintf("snap-%03d", i+1),
			CreatedAt:  time.Now().Add(time.Duration(i) * time.Second).Truncate(time.Second),
			Tables:     []laredo.TableSnapshotInfo{{Table: table, RowCount: 1}},
		}
		_, _ = store.Save(ctx, meta.SnapshotID, meta, map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			table: {{Row: laredo.Row{"id": float64(i)}}},
		})
	}

	// Prune to keep 2.
	if err := store.Prune(ctx, 2, nil); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	descriptors, _ := store.List(ctx, nil)
	if len(descriptors) != 2 {
		t.Errorf("expected 2 snapshots after prune, got %d", len(descriptors))
	}
}
