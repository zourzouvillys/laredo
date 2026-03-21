package fanout

import (
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

func TestJournal_Append(t *testing.T) {
	j := newJournal(100, 0)

	seq := j.append(laredo.ActionInsert, nil, laredo.Row{"id": 1})
	if seq != 1 {
		t.Errorf("expected seq=1, got %d", seq)
	}

	seq = j.append(laredo.ActionUpdate, laredo.Row{"id": 1}, laredo.Row{"id": 1, "name": "x"})
	if seq != 2 {
		t.Errorf("expected seq=2, got %d", seq)
	}

	if j.len() != 2 {
		t.Errorf("expected len=2, got %d", j.len())
	}
	if j.currentSequence() != 2 {
		t.Errorf("expected current=2, got %d", j.currentSequence())
	}
	if j.oldestSequence() != 1 {
		t.Errorf("expected oldest=1, got %d", j.oldestSequence())
	}
}

func TestJournal_EntriesSince(t *testing.T) {
	j := newJournal(100, 0)

	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 1})
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 2})
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 3})

	entries := j.entriesSince(0) // all entries
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	entries = j.entriesSince(1) // after seq 1
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after seq 1, got %d", len(entries))
	}
	if entries[0].Sequence != 2 {
		t.Errorf("expected first entry seq=2, got %d", entries[0].Sequence)
	}

	entries = j.entriesSince(3) // nothing after seq 3
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after seq 3, got %d", len(entries))
	}
}

func TestJournal_PruneBySize(t *testing.T) {
	j := newJournal(3, 0) // max 3 entries

	for i := range 5 {
		j.append(laredo.ActionInsert, nil, laredo.Row{"id": i + 1})
	}

	if j.len() != 3 {
		t.Errorf("expected 3 entries after pruning, got %d", j.len())
	}
	if j.oldestSequence() != 3 {
		t.Errorf("expected oldest=3, got %d", j.oldestSequence())
	}
	if j.currentSequence() != 5 {
		t.Errorf("expected current=5, got %d", j.currentSequence())
	}
}

func TestJournal_PruneByAge(t *testing.T) {
	j := newJournal(0, 50*time.Millisecond) // 50ms max age

	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 1})
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 2})

	time.Sleep(100 * time.Millisecond)

	// New append should trigger age-based pruning.
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 3})

	if j.len() != 1 {
		t.Errorf("expected 1 entry after age pruning, got %d", j.len())
	}
	if j.oldestSequence() != 3 {
		t.Errorf("expected oldest=3, got %d", j.oldestSequence())
	}
}

func TestJournal_Empty(t *testing.T) {
	j := newJournal(100, 0)

	if j.len() != 0 {
		t.Errorf("expected len=0, got %d", j.len())
	}
	if j.currentSequence() != 0 {
		t.Errorf("expected current=0, got %d", j.currentSequence())
	}
	if j.oldestSequence() != 0 {
		t.Errorf("expected oldest=0, got %d", j.oldestSequence())
	}

	entries := j.entriesSince(0)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestJournal_Clear(t *testing.T) {
	j := newJournal(100, 0)
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 1})
	j.append(laredo.ActionInsert, nil, laredo.Row{"id": 2})

	j.clear()

	if j.len() != 0 {
		t.Errorf("expected len=0 after clear, got %d", j.len())
	}
	// Sequence continues from where it was.
	if j.currentSequence() != 2 {
		t.Errorf("expected current=2 after clear, got %d", j.currentSequence())
	}
}
