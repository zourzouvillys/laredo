package laredo_test

import (
	"context"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestSnapshotReplay_Run(t *testing.T) {
	store := &fakeSnapshotStore{}
	table := testutil.SampleTable()

	// Pre-populate store with a snapshot.
	store.saved = append(store.saved, savedSnapshot{
		id: "snap-replay",
		metadata: laredo.SnapshotMetadata{
			SnapshotID: "snap-replay",
			CreatedAt:  time.Now(),
			Tables: []laredo.TableSnapshotInfo{
				{
					Table:    table,
					RowCount: 3,
					Columns:  testutil.SampleColumns(),
				},
			},
		},
		entries: map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			table: {
				{Row: testutil.SampleRow(1, "alice")},
				{Row: testutil.SampleRow(2, "bob")},
				{Row: testutil.SampleRow(3, "charlie")},
			},
		},
	})

	target := memory.NewIndexedTarget()

	result, err := laredo.NewSnapshotReplay(store).
		Target(table, target).
		Run(context.Background(), "snap-replay")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if result.SnapshotID != "snap-replay" {
		t.Errorf("snapshot ID: %s", result.SnapshotID)
	}
	if result.Tables != 1 {
		t.Errorf("tables: %d", result.Tables)
	}
	if result.TotalRows != 3 {
		t.Errorf("total rows: %d", result.TotalRows)
	}
	if result.Duration <= 0 {
		t.Error("expected positive duration")
	}

	// Verify target received the data.
	if target.Count() != 3 {
		t.Fatalf("expected 3 rows in target, got %d", target.Count())
	}
	row, ok := target.Get(2)
	if !ok {
		t.Fatal("expected row with id=2")
	}
	if row["name"] != "bob" {
		t.Errorf("expected bob, got %v", row["name"])
	}
}

func TestSnapshotReplay_MultipleTables(t *testing.T) {
	store := &fakeSnapshotStore{}
	table1 := testutil.SampleTable()
	table2 := laredo.Table("public", "other")

	store.saved = append(store.saved, savedSnapshot{
		id: "snap-multi",
		metadata: laredo.SnapshotMetadata{
			SnapshotID: "snap-multi",
			Tables: []laredo.TableSnapshotInfo{
				{Table: table1, RowCount: 1, Columns: testutil.SampleColumns()},
				{Table: table2, RowCount: 2, Columns: testutil.SampleColumns()},
			},
		},
		entries: map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			table1: {{Row: testutil.SampleRow(1, "a")}},
			table2: {{Row: testutil.SampleRow(10, "x")}, {Row: testutil.SampleRow(11, "y")}},
		},
	})

	t1 := memory.NewIndexedTarget()
	t2 := memory.NewIndexedTarget()

	result, err := laredo.NewSnapshotReplay(store).
		Target(table1, t1).
		Target(table2, t2).
		Run(context.Background(), "snap-multi")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if result.Tables != 2 {
		t.Errorf("expected 2 tables, got %d", result.Tables)
	}
	if result.TotalRows != 3 {
		t.Errorf("expected 3 total rows, got %d", result.TotalRows)
	}
	if t1.Count() != 1 {
		t.Errorf("t1: expected 1 row, got %d", t1.Count())
	}
	if t2.Count() != 2 {
		t.Errorf("t2: expected 2 rows, got %d", t2.Count())
	}
}

func TestSnapshotReplay_SkipsUnmappedTables(t *testing.T) {
	store := &fakeSnapshotStore{}
	table := testutil.SampleTable()
	other := laredo.Table("public", "other")

	store.saved = append(store.saved, savedSnapshot{
		id: "snap-skip",
		metadata: laredo.SnapshotMetadata{
			SnapshotID: "snap-skip",
			Tables: []laredo.TableSnapshotInfo{
				{Table: table, RowCount: 1, Columns: testutil.SampleColumns()},
				{Table: other, RowCount: 1, Columns: testutil.SampleColumns()},
			},
		},
		entries: map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			table: {{Row: testutil.SampleRow(1, "a")}},
			other: {{Row: testutil.SampleRow(2, "b")}},
		},
	})

	target := memory.NewIndexedTarget()

	// Only map table, not other.
	result, err := laredo.NewSnapshotReplay(store).
		Target(table, target).
		Run(context.Background(), "snap-skip")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if result.Tables != 1 {
		t.Errorf("expected 1 table replayed, got %d", result.Tables)
	}
	if target.Count() != 1 {
		t.Errorf("expected 1 row, got %d", target.Count())
	}
}

func TestSnapshotReplay_Start(t *testing.T) {
	store := &fakeSnapshotStore{}
	table := testutil.SampleTable()

	store.saved = append(store.saved, savedSnapshot{
		id: "snap-async",
		metadata: laredo.SnapshotMetadata{
			SnapshotID: "snap-async",
			Tables:     []laredo.TableSnapshotInfo{{Table: table, RowCount: 1, Columns: testutil.SampleColumns()}},
		},
		entries: map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			table: {{Row: testutil.SampleRow(1, "async")}},
		},
	})

	target := memory.NewIndexedTarget()

	ch := laredo.NewSnapshotReplay(store).
		Target(table, target).
		Start(context.Background(), "snap-async")

	result := <-ch
	if result.TotalRows != 1 {
		t.Errorf("expected 1 row, got %d", result.TotalRows)
	}
	if target.Count() != 1 {
		t.Errorf("expected 1 row in target, got %d", target.Count())
	}
}

func TestSnapshotReplay_NotFound(t *testing.T) {
	store := &fakeSnapshotStore{}

	_, err := laredo.NewSnapshotReplay(store).
		Run(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent snapshot")
	}
}
