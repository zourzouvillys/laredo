package fanout

import (
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
)

// JournalEntry is a single entry in the change journal.
type JournalEntry struct {
	Sequence  int64
	Timestamp time.Time
	Action    laredo.ChangeAction
	OldValues laredo.Row
	NewValues laredo.Row
}

// journal is a bounded circular buffer of change entries with monotonic
// sequence numbers. It supports pruning by max entries and max age.
type journal struct {
	mu         sync.RWMutex
	entries    []JournalEntry
	maxEntries int
	maxAge     time.Duration
	nextSeq    int64
}

func newJournal(maxEntries int, maxAge time.Duration) *journal {
	return &journal{
		maxEntries: maxEntries,
		maxAge:     maxAge,
		nextSeq:    1,
	}
}

// append adds a new entry to the journal and returns its sequence number.
func (j *journal) append(action laredo.ChangeAction, oldValues, newValues laredo.Row) int64 {
	j.mu.Lock()
	defer j.mu.Unlock()

	seq := j.nextSeq
	j.nextSeq++

	j.entries = append(j.entries, JournalEntry{
		Sequence:  seq,
		Timestamp: time.Now(),
		Action:    action,
		OldValues: oldValues,
		NewValues: newValues,
	})

	j.pruneLocked()
	return seq
}

// entriesSince returns all entries with sequence > afterSeq.
func (j *journal) entriesSince(afterSeq int64) []JournalEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()

	var result []JournalEntry
	for _, e := range j.entries {
		if e.Sequence > afterSeq {
			result = append(result, e)
		}
	}
	return result
}

// currentSequence returns the latest sequence number (0 if empty).
func (j *journal) currentSequence() int64 {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.nextSeq - 1
}

// oldestSequence returns the oldest retained sequence number (0 if empty).
func (j *journal) oldestSequence() int64 {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if len(j.entries) == 0 {
		return 0
	}
	return j.entries[0].Sequence
}

// len returns the number of entries in the journal.
func (j *journal) len() int {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return len(j.entries)
}

// clear removes all entries and resets the sequence to the current value.
func (j *journal) clear() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = nil
}

// pruneLocked removes entries that exceed max_entries or max_age.
// Must be called with mu held.
func (j *journal) pruneLocked() {
	// Prune by size.
	if j.maxEntries > 0 && len(j.entries) > j.maxEntries {
		excess := len(j.entries) - j.maxEntries
		j.entries = j.entries[excess:]
	}

	// Prune by age.
	if j.maxAge > 0 {
		cutoff := time.Now().Add(-j.maxAge)
		i := 0
		for i < len(j.entries) && j.entries[i].Timestamp.Before(cutoff) {
			i++
		}
		if i > 0 {
			j.entries = j.entries[i:]
		}
	}
}
