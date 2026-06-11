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
	// Position is the opaque source position (e.g. PostgreSQL WAL LSN) of the
	// change. It is the cross-instance-stable coordinate clients use to resume
	// on a different server. May be nil for entries appended before the source
	// position is known (e.g. baseline rows, later backfilled).
	Position laredo.Position
}

// journal is a bounded circular buffer of change entries with monotonic
// sequence numbers. It supports pruning by max entries and max age.
type journal struct {
	mu         sync.RWMutex
	entries    []JournalEntry
	maxEntries int
	maxAge     time.Duration
	nextSeq    int64
	pins       map[string]int64 // clientID → minimum sequence to retain
}

func newJournal(maxEntries int, maxAge time.Duration) *journal {
	return &journal{
		maxEntries: maxEntries,
		maxAge:     maxAge,
		nextSeq:    1,
		pins:       make(map[string]int64),
	}
}

// pin prevents pruning entries at or after the given sequence for a client.
func (j *journal) pin(clientID string, seq int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pins[clientID] = seq
}

// unpin removes a client's journal pin.
func (j *journal) unpin(clientID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.pins, clientID)
}

// append adds a new entry to the journal and returns its sequence number.
func (j *journal) append(action laredo.ChangeAction, oldValues, newValues laredo.Row, position laredo.Position) int64 {
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
		Position:  position,
	})

	j.pruneLocked()
	return seq
}

// backfillNilPositions stamps the given position onto every retained entry that
// has no position yet. Used after baseline completes, when the baseline end
// position becomes known, so baseline-derived entries carry a comparable
// coordinate for cross-instance resume.
func (j *journal) backfillNilPositions(position laredo.Position) {
	if position == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.entries {
		if j.entries[i].Position == nil {
			j.entries[i].Position = position
		}
	}
}

// resumeSeqForPosition returns the journal sequence to resume *after* for a
// client whose last applied position is the given one, plus whether that
// position is still covered by the retained journal.
//
// The returned sequence is suitable for entriesSince: streaming resumes with
// the first entry whose position is strictly newer than the client's. covered
// is false when the client's position is older than the oldest retained entry
// (entries it still needs were pruned, so it must re-snapshot). cmp compares two
// positions (e.g. SyncSource.ComparePositions). Entries with a nil position are
// skipped (treated as older than any real position).
func (j *journal) resumeSeqForPosition(position laredo.Position, cmp func(a, b laredo.Position) int) (resumeSeq int64, covered bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	if len(j.entries) == 0 {
		// Empty journal: nothing to send; resume from the current head.
		return j.nextSeq - 1, true
	}

	// If the oldest retained entry is already strictly newer than the client's
	// position, we may have pruned entries the client still needs.
	if oldest := j.entries[0].Position; oldest != nil && cmp(oldest, position) > 0 {
		return 0, false
	}

	// Resume after the last entry whose position is <= the client's position.
	resumeSeq = j.entries[0].Sequence - 1
	for _, e := range j.entries {
		if e.Position != nil && cmp(e.Position, position) <= 0 {
			resumeSeq = e.Sequence
		}
	}
	return resumeSeq, true
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
// Respects active pins — never prunes entries at or after the lowest pin.
// Must be called with mu held.
func (j *journal) pruneLocked() {
	// Find the lowest pin sequence.
	var minPin int64
	for _, seq := range j.pins {
		if minPin == 0 || seq < minPin {
			minPin = seq
		}
	}

	// Prune by size.
	if j.maxEntries > 0 && len(j.entries) > j.maxEntries {
		excess := len(j.entries) - j.maxEntries
		// Don't prune past the pin.
		if minPin > 0 {
			for excess > 0 && len(j.entries) > 0 && j.entries[0].Sequence < minPin {
				excess--
			}
			if excess <= 0 {
				// All prunable entries are before the pin — prune up to the pin.
				i := 0
				for i < len(j.entries) && j.entries[i].Sequence < minPin && len(j.entries)-i > j.maxEntries {
					i++
				}
				if i > 0 {
					j.entries = j.entries[i:]
				}
				return
			}
		}
		j.entries = j.entries[excess:]
	}

	// Prune by age.
	if j.maxAge > 0 {
		cutoff := time.Now().Add(-j.maxAge)
		i := 0
		for i < len(j.entries) && j.entries[i].Timestamp.Before(cutoff) {
			// Don't prune past the pin.
			if minPin > 0 && j.entries[i].Sequence >= minPin {
				break
			}
			i++
		}
		if i > 0 {
			j.entries = j.entries[i:]
		}
	}
}
