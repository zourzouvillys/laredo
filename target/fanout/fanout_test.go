package fanout_test

import (
	"context"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func initTarget(t *testing.T) *fanout.Target {
	t.Helper()
	target := fanout.New(fanout.JournalMaxEntries(1000))
	if err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns()); err != nil {
		t.Fatalf("OnInit: %v", err)
	}
	return target
}

func TestTarget_BaselineFlow(t *testing.T) {
	target := initTarget(t)
	ctx := context.Background()

	if err := target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(1, "alice")); err != nil {
		t.Fatalf("OnBaselineRow: %v", err)
	}
	if err := target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(2, "bob")); err != nil {
		t.Fatalf("OnBaselineRow: %v", err)
	}
	if err := target.OnBaselineComplete(ctx, testutil.SampleTable()); err != nil {
		t.Fatalf("OnBaselineComplete: %v", err)
	}

	if target.Count() != 2 {
		t.Errorf("expected 2 rows, got %d", target.Count())
	}
	if !target.IsReady() {
		t.Error("expected ready=true after baseline complete")
	}
	if target.JournalSequence() != 2 {
		t.Errorf("expected journal seq=2, got %d", target.JournalSequence())
	}
}

func TestTarget_StreamingChanges(t *testing.T) {
	target := initTarget(t)
	ctx := context.Background()

	_ = target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	_ = target.OnBaselineComplete(ctx, testutil.SampleTable())

	// Insert.
	if err := target.OnInsert(ctx, testutil.SampleTable(), testutil.SampleRow(2, "bob")); err != nil {
		t.Fatalf("OnInsert: %v", err)
	}
	if target.Count() != 2 {
		t.Errorf("expected 2 rows after insert, got %d", target.Count())
	}

	// Update.
	if err := target.OnUpdate(ctx, testutil.SampleTable(), testutil.SampleRow(1, "alice-updated"), laredo.Row{"id": 1}); err != nil {
		t.Fatalf("OnUpdate: %v", err)
	}
	if target.Count() != 2 {
		t.Errorf("expected 2 rows after update, got %d", target.Count())
	}

	// Delete.
	if err := target.OnDelete(ctx, testutil.SampleTable(), laredo.Row{"id": 2}); err != nil {
		t.Fatalf("OnDelete: %v", err)
	}
	if target.Count() != 1 {
		t.Errorf("expected 1 row after delete, got %d", target.Count())
	}
}

func TestTarget_Truncate(t *testing.T) {
	target := initTarget(t)
	ctx := context.Background()

	_ = target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	_ = target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	_ = target.OnBaselineComplete(ctx, testutil.SampleTable())

	if err := target.OnTruncate(ctx, testutil.SampleTable()); err != nil {
		t.Fatalf("OnTruncate: %v", err)
	}
	if target.Count() != 0 {
		t.Errorf("expected 0 rows after truncate, got %d", target.Count())
	}
}

func TestTarget_JournalTracking(t *testing.T) {
	target := initTarget(t)
	ctx := context.Background()

	_ = target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	_ = target.OnBaselineComplete(ctx, testutil.SampleTable())

	beforeSeq := target.JournalSequence()

	_ = target.OnInsert(ctx, testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	_ = target.OnInsert(ctx, testutil.SampleTable(), testutil.SampleRow(3, "charlie"))

	entries := target.JournalEntriesSince(beforeSeq)
	if len(entries) != 2 {
		t.Errorf("expected 2 journal entries since baseline, got %d", len(entries))
	}
	if entries[0].Action != laredo.ActionInsert {
		t.Errorf("expected INSERT, got %v", entries[0].Action)
	}
}

func TestTarget_ExportSnapshot(t *testing.T) {
	target := initTarget(t)
	ctx := context.Background()

	_ = target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	_ = target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	_ = target.OnBaselineComplete(ctx, testutil.SampleTable())

	entries, err := target.ExportSnapshot(ctx)
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 snapshot entries, got %d", len(entries))
	}
}

func TestTarget_IsDurable(t *testing.T) {
	target := fanout.New()
	if !target.IsDurable() {
		t.Error("expected IsDurable=true")
	}
}

func TestTarget_TakeSnapshot(t *testing.T) {
	target := initTarget(t)
	ctx := context.Background()

	_ = target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	_ = target.OnBaselineRow(ctx, testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	_ = target.OnBaselineComplete(ctx, testutil.SampleTable())

	snap := target.TakeSnapshot()
	if snap.RowCount != 2 {
		t.Errorf("expected 2 rows in snapshot, got %d", snap.RowCount)
	}
	if snap.Sequence != target.JournalSequence() {
		t.Errorf("expected sequence %d, got %d", target.JournalSequence(), snap.Sequence)
	}
	if snap.ID == "" {
		t.Error("expected non-empty snapshot ID")
	}

	// Should appear in list.
	snaps := target.ListSnapshots()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].ID != snap.ID {
		t.Errorf("expected ID=%s, got %s", snap.ID, snaps[0].ID)
	}

	// Latest should return it.
	latest := target.LatestSnapshot()
	if latest == nil {
		t.Fatal("expected non-nil latest snapshot")
	}
	if latest.ID != snap.ID {
		t.Errorf("expected latest ID=%s, got %s", snap.ID, latest.ID)
	}
}

func TestTarget_SnapshotRetention_KeepCount(t *testing.T) {
	target := fanout.New(
		fanout.JournalMaxEntries(1000),
		fanout.SnapshotKeepCount(2),
	)
	_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Take 4 snapshots.
	for range 4 {
		target.TakeSnapshot()
	}

	// Should retain only 2 (keep_count).
	snaps := target.ListSnapshots()
	if len(snaps) != 2 {
		t.Errorf("expected 2 snapshots (keep_count=2), got %d", len(snaps))
	}
}

func TestTarget_SnapshotRetention_MaxAge(t *testing.T) {
	target := fanout.New(
		fanout.JournalMaxEntries(1000),
		fanout.SnapshotMaxAge(50*time.Millisecond),
	)
	_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Take a snapshot.
	target.TakeSnapshot()
	if len(target.ListSnapshots()) != 1 {
		t.Fatal("expected 1 snapshot")
	}

	// Wait for it to expire.
	time.Sleep(100 * time.Millisecond)

	// Take another — old one should be pruned.
	target.TakeSnapshot()
	snaps := target.ListSnapshots()
	if len(snaps) != 1 {
		t.Errorf("expected 1 snapshot after age pruning, got %d", len(snaps))
	}
}

func TestTarget_SnapshotScheduler(t *testing.T) {
	target := fanout.New(
		fanout.JournalMaxEntries(1000),
		fanout.SnapshotInterval(50*time.Millisecond),
		fanout.SnapshotKeepCount(5),
	)
	_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	go target.StartSnapshotScheduler(ctx)
	<-ctx.Done()

	// Should have taken several snapshots.
	snaps := target.ListSnapshots()
	if len(snaps) < 2 {
		t.Errorf("expected at least 2 periodic snapshots, got %d", len(snaps))
	}
}
