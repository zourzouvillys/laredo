// Package fanout implements a SyncTarget that multiplexes one source to N gRPC clients
// via snapshot + journal + live stream replication.
package fanout

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
)

// Snapshot represents a point-in-time snapshot of the fan-out state.
type Snapshot struct {
	ID        string
	Sequence  int64
	RowCount  int64
	Rows      []laredo.Row
	CreatedAt time.Time
}

// Target implements laredo.SyncTarget as a replication fan-out multiplexer.
// It maintains an in-memory copy of all rows and a bounded change journal
// that clients can use to catch up.
type Target struct {
	cfg config

	mu        sync.RWMutex
	store     map[string]laredo.Row // keyed by composite PK
	j         *journal
	ready     bool
	table     laredo.TableIdentifier
	cols      []laredo.ColumnDefinition
	snapshots []Snapshot // ordered newest-first
}

type config struct {
	maxJournalEntries int
	maxJournalAge     time.Duration
	snapshotInterval  time.Duration
	snapshotKeepCount int
	snapshotMaxAge    time.Duration
}

// Option configures the fan-out target.
type Option func(*config)

// JournalMaxEntries sets the maximum number of journal entries to retain.
func JournalMaxEntries(n int) Option {
	return func(c *config) { c.maxJournalEntries = n }
}

// JournalMaxAge sets the maximum age of journal entries before pruning.
func JournalMaxAge(d time.Duration) Option {
	return func(c *config) { c.maxJournalAge = d }
}

// SnapshotInterval sets how often periodic snapshots are taken.
// Zero (default) disables periodic snapshots.
func SnapshotInterval(d time.Duration) Option {
	return func(c *config) { c.snapshotInterval = d }
}

// SnapshotKeepCount sets the maximum number of snapshots to retain.
func SnapshotKeepCount(n int) Option {
	return func(c *config) { c.snapshotKeepCount = n }
}

// SnapshotMaxAge sets the maximum age for snapshot retention.
func SnapshotMaxAge(d time.Duration) Option {
	return func(c *config) { c.snapshotMaxAge = d }
}

// New creates a new replication fan-out target.
func New(opts ...Option) *Target {
	cfg := config{
		maxJournalEntries: 100000,
		maxJournalAge:     24 * time.Hour,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Target{
		cfg:   cfg,
		store: make(map[string]laredo.Row),
		j:     newJournal(cfg.maxJournalEntries, cfg.maxJournalAge),
	}
}

var _ laredo.SyncTarget = (*Target)(nil)

//nolint:revive // implements SyncTarget.
func (t *Target) OnInit(_ context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.table = table
	t.cols = columns
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnBaselineRow(_ context.Context, _ laredo.TableIdentifier, row laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := t.buildKey(row)
	t.store[key] = row
	t.j.append(laredo.ActionInsert, nil, row)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnBaselineComplete(_ context.Context, _ laredo.TableIdentifier) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ready = true
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnInsert(_ context.Context, _ laredo.TableIdentifier, row laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := t.buildKey(row)
	t.store[key] = row
	t.j.append(laredo.ActionInsert, nil, row)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnUpdate(_ context.Context, _ laredo.TableIdentifier, row laredo.Row, identity laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := t.buildKey(row)
	old := t.store[key]
	t.store[key] = row
	t.j.append(laredo.ActionUpdate, old, row)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnDelete(_ context.Context, _ laredo.TableIdentifier, identity laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := t.buildKey(identity)
	old := t.store[key]
	delete(t.store, key)
	t.j.append(laredo.ActionDelete, old, nil)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnTruncate(_ context.Context, _ laredo.TableIdentifier) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.store = make(map[string]laredo.Row)
	t.j.append(laredo.ActionTruncate, nil, nil)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) IsDurable() bool { return true }

//nolint:revive // implements SyncTarget.
func (t *Target) OnSchemaChange(_ context.Context, _ laredo.TableIdentifier, _, newColumns []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cols = newColumns
	// Append a schema change marker to the journal so clients know to re-read schema.
	t.j.append(laredo.ActionTruncate, nil, nil) // schema changes use truncate action as marker
	return laredo.SchemaChangeResponse{Action: laredo.SchemaReBaseline}
}

//nolint:revive // implements SyncTarget.
func (t *Target) ExportSnapshot(_ context.Context) ([]laredo.SnapshotEntry, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	entries := make([]laredo.SnapshotEntry, 0, len(t.store))
	for _, row := range t.store {
		entries = append(entries, laredo.SnapshotEntry{Row: row})
	}
	return entries, nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) RestoreSnapshot(_ context.Context, _ laredo.TableSnapshotInfo, entries []laredo.SnapshotEntry) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.store = make(map[string]laredo.Row, len(entries))
	for _, e := range entries {
		key := t.buildKey(e.Row)
		t.store[key] = e.Row
	}
	t.ready = true
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) SupportsConsistentSnapshot() bool { return false }

//nolint:revive // implements SyncTarget.
func (t *Target) OnClose(_ context.Context, _ laredo.TableIdentifier) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ready = false
	return nil
}

// Count returns the number of rows in the in-memory state.
func (t *Target) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.store)
}

// IsReady reports whether the target has completed baseline loading.
func (t *Target) IsReady() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ready
}

// JournalSequence returns the current journal sequence number.
func (t *Target) JournalSequence() int64 {
	return t.j.currentSequence()
}

// JournalOldestSequence returns the oldest retained journal sequence.
func (t *Target) JournalOldestSequence() int64 {
	return t.j.oldestSequence()
}

// JournalLen returns the number of entries in the journal.
func (t *Target) JournalLen() int {
	return t.j.len()
}

// JournalEntriesSince returns journal entries after the given sequence.
func (t *Target) JournalEntriesSince(afterSeq int64) []JournalEntry {
	return t.j.entriesSince(afterSeq)
}

// TakeSnapshot captures the current in-memory state tagged with the current
// journal sequence. Used for client bootstrapping and retention.
func (t *Target) TakeSnapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	rows := make([]laredo.Row, 0, len(t.store))
	for _, row := range t.store {
		rows = append(rows, row)
	}

	snap := Snapshot{
		ID:        fmt.Sprintf("fanout-%d-%d", t.j.currentSequence(), time.Now().UnixMilli()),
		Sequence:  t.j.currentSequence(),
		RowCount:  int64(len(rows)),
		Rows:      rows,
		CreatedAt: time.Now(),
	}

	// Insert at front (newest first) and prune.
	t.snapshots = append([]Snapshot{snap}, t.snapshots...)
	t.pruneSnapshots()

	return snap
}

// ListSnapshots returns available snapshots (newest first).
func (t *Target) ListSnapshots() []Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]Snapshot, len(t.snapshots))
	copy(result, t.snapshots)
	return result
}

// LatestSnapshot returns the most recent snapshot, or nil if none.
func (t *Target) LatestSnapshot() *Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.snapshots) == 0 {
		return nil
	}
	snap := t.snapshots[0]
	return &snap
}

// pruneSnapshots removes old snapshots based on keep_count and max_age.
// Must be called with mu held for write.
func (t *Target) pruneSnapshots() {
	if t.cfg.snapshotKeepCount > 0 && len(t.snapshots) > t.cfg.snapshotKeepCount {
		t.snapshots = t.snapshots[:t.cfg.snapshotKeepCount]
	}
	if t.cfg.snapshotMaxAge > 0 {
		cutoff := time.Now().Add(-t.cfg.snapshotMaxAge)
		for i, s := range t.snapshots {
			if s.CreatedAt.Before(cutoff) {
				t.snapshots = t.snapshots[:i]
				break
			}
		}
	}
}

// StartSnapshotScheduler starts a background goroutine that periodically
// takes snapshots. Blocks until the context is cancelled.
func (t *Target) StartSnapshotScheduler(ctx context.Context) {
	if t.cfg.snapshotInterval <= 0 {
		return
	}

	ticker := time.NewTicker(t.cfg.snapshotInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if t.IsReady() {
				t.TakeSnapshot()
			}
		}
	}
}

// buildKey creates a composite key from the row's PK columns.
func (t *Target) buildKey(row laredo.Row) string {
	var parts []string
	for _, col := range t.cols {
		if col.PrimaryKey {
			parts = append(parts, fmt.Sprintf("%v", row[col.Name]))
		}
	}
	if len(parts) == 0 {
		// Fallback to "id" field if no PK columns defined.
		if id, ok := row["id"]; ok {
			return fmt.Sprintf("%v", id)
		}
	}
	return strings.Join(parts, "\x00")
}
