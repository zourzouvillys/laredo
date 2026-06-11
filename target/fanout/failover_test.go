package fanout

import (
	"context"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

// uint64Cmp compares two uint64 positions, matching SyncSource.ComparePositions.
func uint64Cmp(a, b laredo.Position) int {
	pa, pb := a.(uint64), b.(uint64)
	switch {
	case pa < pb:
		return -1
	case pa > pb:
		return 1
	default:
		return 0
	}
}

func TestJournal_ResumeSeqForPosition(t *testing.T) {
	j := newJournal(0, 0)
	// seq 1..4 with positions 10,20,30,40.
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 1}, uint64(10))
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 2}, uint64(20))
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 3}, uint64(30))
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 4}, uint64(40))

	tests := []struct {
		name    string
		pos     uint64
		wantSeq int64
		covered bool
	}{
		{"exact match mid-journal", 20, 2, true}, // resume after seq 2 → stream 3,4
		{"between entries", 25, 2, true},         // resume after the entry at 20
		{"at head", 40, 4, true},                 // nothing newer to send
		{"ahead of head", 99, 4, true},           // client ahead; wait for journal to advance
		{"older than oldest", 5, 0, false},       // pruned region → must re-snapshot
		{"equals oldest", 10, 1, true},           // resume after seq 1
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seq, covered := j.resumeSeqForPosition(tc.pos, uint64Cmp)
			if covered != tc.covered {
				t.Fatalf("covered = %v, want %v", covered, tc.covered)
			}
			if covered && seq != tc.wantSeq {
				t.Fatalf("resumeSeq = %d, want %d", seq, tc.wantSeq)
			}
		})
	}
}

func TestJournal_ResumeSeqForPosition_Empty(t *testing.T) {
	j := newJournal(0, 0)
	seq, covered := j.resumeSeqForPosition(uint64(5), uint64Cmp)
	if !covered {
		t.Fatal("empty journal should report covered")
	}
	if seq != 0 {
		t.Fatalf("resumeSeq = %d, want 0", seq)
	}
}

func TestJournal_BackfillNilPositions(t *testing.T) {
	j := newJournal(0, 0)
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 1}, nil)        // baseline (nil)
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 2}, nil)        // baseline (nil)
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 3}, uint64(50)) // streamed

	j.backfillNilPositions(uint64(42))

	for _, e := range j.entriesSince(0) {
		if e.Position == nil {
			t.Fatalf("entry seq %d still has nil position", e.Sequence)
		}
	}
	// The previously-nil entries should now carry the baseline position; the
	// already-stamped entry must be left untouched.
	entries := j.entriesSince(0)
	if entries[0].Position.(uint64) != 42 || entries[1].Position.(uint64) != 42 {
		t.Fatalf("baseline entries not backfilled: %v, %v", entries[0].Position, entries[1].Position)
	}
	if entries[2].Position.(uint64) != 50 {
		t.Fatalf("streamed entry position changed: %v", entries[2].Position)
	}
}

func TestTarget_PositionRecordedViaSetChangePosition(t *testing.T) {
	tgt := New()
	ctx := context.Background()
	_ = tgt.OnInit(ctx, laredo.Table("public", "t"), nil)
	_ = tgt.OnBaselineComplete(ctx, laredo.Table("public", "t"))

	// Engine sets the position immediately before each On* call.
	tgt.SetChangePosition(uint64(100))
	_ = tgt.OnInsert(ctx, laredo.Table("public", "t"), laredo.Row{"id": 1})
	tgt.SetChangePosition(uint64(200))
	_ = tgt.OnUpdate(ctx, laredo.Table("public", "t"), laredo.Row{"id": 1, "v": 2}, laredo.Row{"id": 1})

	seq, covered := tgt.ResumeSequenceForPosition(uint64(100), uint64Cmp)
	if !covered {
		t.Fatal("position 100 should be covered")
	}
	entries := tgt.JournalEntriesSince(seq)
	if len(entries) != 1 || entries[0].Position.(uint64) != 200 {
		t.Fatalf("expected to resume to the single entry at position 200, got %+v", entries)
	}
}

func TestTarget_Drain(t *testing.T) {
	tgt := New()

	if tgt.IsDraining() {
		t.Fatal("new target should not be draining")
	}

	// Draining() must be ready before Drain so a waiter never misses the close.
	ch := tgt.Draining()
	select {
	case <-ch:
		t.Fatal("channel closed before drain")
	default:
	}

	deadline := time.Now().Add(time.Minute)
	tgt.Drain("admin", deadline)

	if !tgt.IsDraining() {
		t.Fatal("target should be draining")
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("drain channel not closed")
	}

	reason, gotDeadline, draining := tgt.DrainInfo()
	if reason != "admin" || !draining || !gotDeadline.Equal(deadline) {
		t.Fatalf("DrainInfo = (%q, %v, %v)", reason, gotDeadline, draining)
	}

	// Drain is idempotent — a second call must not panic (double close).
	tgt.Drain("again", time.Time{})
}
