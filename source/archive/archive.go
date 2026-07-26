// Package archive provides a laredo SyncSource that replays a snapshotter
// archive — a base snapshot plus a chain of diffs, indexed by a manifest, on a
// destination such as local disk — instead of connecting to a live database.
//
// It lets an engine start with no PostgreSQL: for an offline backup/snapshot, to
// come up immediately from a file, or to seed local development from a committed
// archive. It is the read-side inverse of the snapshotter Writer, built on
// snapshotter.Reader; see docs/edr/0006-archive-source.md.
//
// One Source serves one table (a manifest is per-table). Baseline reconstructs
// the table's current state at the archive head; Stream then replays diffs
// appended after that point. In follow mode Stream keeps polling for new diffs
// and, when the archive is wholesale replaced (a new epoch whose base no longer
// continues the consumer's position), returns laredo.ErrReBaselineRequired so the
// engine re-baselines against the new archive.
package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/internal/lsn"
	"github.com/zourzouvillys/laredo/snapshotter"
)

// Source is a laredo.SyncSource backed by a snapshotter archive.
type Source struct {
	cfg   config
	table laredo.TableIdentifier

	mu        sync.Mutex
	state     laredo.SourceState
	lastAcked string
	headTime  time.Time // manifest head timestamp, for lag reporting
	signal    chan struct{}
}

var _ laredo.SyncSource = (*Source)(nil)

// New creates an archive Source. WithReader and Table are required.
func New(opts ...Option) *Source {
	cfg := config{
		cmp:          lsn.Compare,
		ordering:     laredo.TotalOrder,
		pollInterval: 5 * time.Second,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Source{
		cfg:    cfg,
		table:  laredo.Table(cfg.schema, cfg.table),
		state:  laredo.SourceConnecting,
		signal: make(chan struct{}, 1),
	}
}

// Init resolves the served table, loads the table manifest, and reports the
// table's columns — from the manifest's recorded schema when present, otherwise
// inferred from a snapshot row (older archives). It performs no streaming.
func (s *Source) Init(ctx context.Context, cfg laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	if s.cfg.reader == nil {
		return nil, errors.New("archive source: no reader configured (use WithReader)")
	}
	if err := s.resolveTable(cfg.Tables); err != nil {
		return nil, err
	}
	m, err := s.cfg.reader.LoadManifest(ctx)
	if err != nil {
		return nil, fmt.Errorf("archive source: load manifest for %s: %w", s.table, err)
	}
	cols, err := s.columns(ctx, m)
	if err != nil {
		return nil, err
	}
	s.setState(laredo.SourceConnected)
	return map[laredo.TableIdentifier][]laredo.ColumnDefinition{s.table: cols}, nil
}

// resolveTable fixes the single table this source serves. An explicit Table
// option wins; otherwise the source adopts the one table the engine bound to it,
// so config need not repeat the table already declared in its pipeline. A
// snapshotter manifest is per-table, so exactly one table is required.
func (s *Source) resolveTable(tables []laredo.TableIdentifier) error {
	if s.table != (laredo.TableIdentifier{}) {
		return nil
	}
	if len(tables) != 1 {
		return fmt.Errorf("archive source: serves exactly one table, but %d were configured", len(tables))
	}
	s.table = tables[0]
	return nil
}

// columns returns the table's schema: the manifest's recorded columns when
// present, else inferred from the newest base snapshot's rows.
func (s *Source) columns(ctx context.Context, m *snapshotter.Manifest) ([]laredo.ColumnDefinition, error) {
	if len(m.Columns) > 0 {
		return m.Columns, nil
	}
	plan, err := s.cfg.reader.Plan(m, "", s.cfg.cmp)
	if err != nil {
		return nil, fmt.Errorf("archive source: plan for schema inference: %w", err)
	}
	if plan == nil || plan.Snapshot == nil {
		return nil, nil // empty archive; columns unknown until data arrives
	}
	rows, err := s.cfg.reader.ReadSnapshot(ctx, *plan.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("archive source: read snapshot for schema inference: %w", err)
	}
	return inferColumns(rows, s.cfg.keyFields), nil
}

// inferColumns derives column definitions from a snapshot row when the manifest
// carries no schema (older archives). It records column names (deterministically
// ordered) and marks the configured key fields as primary keys; types are left
// unset since the archive does not record them.
func inferColumns(rows []laredo.Row, keyFields []string) []laredo.ColumnDefinition {
	if len(rows) == 0 {
		return nil
	}
	keyOrdinal := make(map[string]int, len(keyFields))
	for i, k := range keyFields {
		keyOrdinal[k] = i + 1
	}
	names := make([]string, 0, len(rows[0]))
	for name := range rows[0] {
		names = append(names, name)
	}
	sort.Strings(names)
	cols := make([]laredo.ColumnDefinition, 0, len(names))
	for i, name := range names {
		col := laredo.ColumnDefinition{Name: name, Nullable: true, OrdinalPosition: i + 1}
		if ord, ok := keyOrdinal[name]; ok {
			col.PrimaryKey = true
			col.PrimaryKeyOrdinal = ord
		}
		cols = append(cols, col)
	}
	return cols
}

// ValidateTables checks the requested tables are the one this source serves.
func (s *Source) ValidateTables(_ context.Context, tables []laredo.TableIdentifier) []laredo.ValidationError {
	var errs []laredo.ValidationError
	for i := range tables {
		t := tables[i]
		if t != s.table {
			errs = append(errs, laredo.ValidationError{
				Table:   &t,
				Code:    "TABLE_NOT_SERVED",
				Message: fmt.Sprintf("archive source serves only %s", s.table),
			})
		}
	}
	return errs
}

// Baseline reconstructs the table's full state at the archive head and emits it,
// returning the head position. Changes appended after it are delivered by Stream.
// An empty archive (no reachable base snapshot) yields an empty baseline; in
// follow mode Stream re-baselines once a snapshot appears.
func (s *Source) Baseline(ctx context.Context, _ []laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	m, err := s.cfg.reader.LoadManifest(ctx)
	if err != nil {
		return nil, fmt.Errorf("archive source: load manifest: %w", err)
	}
	s.rememberHead(m)
	rec, err := s.cfg.reader.ReconstructAsOf(ctx, m.HeadPosition, s.cfg.keyFields, s.cfg.cmp)
	if err != nil {
		return nil, fmt.Errorf("archive source: reconstruct baseline: %w", err)
	}
	if rec == nil {
		return m.HeadPosition, nil
	}
	for _, row := range rec.Rows {
		rowCallback(s.table, row)
	}
	s.setLastAcked(rec.Position)
	return rec.Position, nil
}

// Stream replays diffs after `from` to the handler. It emits each diff's changes
// in order, advancing the position to the diff's boundary. In follow mode it then
// polls the manifest for new diffs; otherwise it returns when it reaches the
// head. When the head has moved but no diff chain continues `from` — the archive
// was re-based, pruned, or wholesale replaced — it returns
// laredo.ErrReBaselineRequired so the engine re-baselines.
func (s *Source) Stream(ctx context.Context, from laredo.Position, handler laredo.ChangeHandler) error {
	fromStr, _ := from.(string)
	s.setState(laredo.SourceStreaming)
	for {
		switch s.State() {
		case laredo.SourceClosed:
			return nil
		case laredo.SourcePaused:
			if err := s.waitPaused(ctx); err != nil {
				return err
			}
			continue
		}

		m, err := s.cfg.reader.LoadManifest(ctx)
		if err != nil {
			if s.cfg.follow && errors.Is(err, snapshotter.ErrManifestNotFound) {
				if werr := s.waitPoll(ctx); werr != nil {
					return werr
				}
				continue
			}
			return fmt.Errorf("archive source: load manifest: %w", err)
		}
		s.rememberHead(m)

		if s.cfg.cmp(m.HeadPosition, fromStr) > 0 {
			next, err := s.drain(ctx, m, fromStr, handler)
			if err != nil {
				return err
			}
			fromStr = next
			continue // drain any further appends before waiting
		}

		if !s.cfg.follow {
			return nil
		}
		if err := s.waitPoll(ctx); err != nil {
			return err
		}
	}
}

// drain emits the diff-only continuation from `fromStr` to the manifest head and
// returns the new position. A clean continuation has plan.Snapshot == nil; any
// other shape means the head moved on without a chain we can follow (re-base,
// prune, or wholesale replacement), so it returns ErrReBaselineRequired.
func (s *Source) drain(ctx context.Context, m *snapshotter.Manifest, fromStr string, handler laredo.ChangeHandler) (string, error) {
	plan, err := s.cfg.reader.Plan(m, fromStr, s.cfg.cmp)
	if err != nil {
		return fromStr, fmt.Errorf("archive source: plan from %q: %w", fromStr, err)
	}
	if plan == nil || plan.Snapshot != nil {
		return fromStr, laredo.ErrReBaselineRequired
	}
	for _, d := range plan.Diffs {
		changes, err := s.cfg.reader.ReadDiff(ctx, d)
		if err != nil {
			return fromStr, fmt.Errorf("archive source: read diff %q→%q: %w", fromPosition(d), d.ToPosition, err)
		}
		for _, ch := range changes {
			if err := handler.OnChange(s.toEvent(ch, d.ToPosition)); err != nil {
				return fromStr, err
			}
		}
		fromStr = d.ToPosition
		s.setLastAcked(fromStr)
	}
	return fromStr, nil
}

func (s *Source) toEvent(ch snapshotter.Change, pos string) laredo.ChangeEvent {
	ev := laredo.ChangeEvent{Table: s.table, Action: ch.Action, Position: pos, Timestamp: time.Now()}
	switch ch.Action {
	case laredo.ActionInsert:
		ev.NewValues = ch.New
	case laredo.ActionUpdate:
		ev.OldValues = ch.Old
		ev.NewValues = ch.New
	case laredo.ActionDelete:
		ev.OldValues = ch.Old
	case laredo.ActionTruncate:
		// no row values
	}
	return ev
}

// Ack records the durable position, persisting it to the state file when resume
// is enabled so a restart continues from it.
func (s *Source) Ack(_ context.Context, position laredo.Position) error {
	p, ok := position.(string)
	if !ok {
		return nil
	}
	s.setLastAcked(p)
	if s.cfg.statePath == "" {
		return nil
	}
	tmp := s.cfg.statePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(p), 0o600); err != nil {
		return fmt.Errorf("archive source: write state: %w", err)
	}
	if err := os.Rename(tmp, s.cfg.statePath); err != nil {
		return fmt.Errorf("archive source: commit state: %w", err)
	}
	return nil
}

// SupportsResume reports whether resume is enabled (a state path is configured).
func (s *Source) SupportsResume() bool { return s.cfg.statePath != "" }

// LastAckedPosition returns the position persisted to the state file, or nil when
// resume is disabled or nothing has been ACKed yet.
func (s *Source) LastAckedPosition(_ context.Context) (laredo.Position, error) {
	if s.cfg.statePath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(s.cfg.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("archive source: read state: %w", err)
	}
	p := string(data)
	if p == "" {
		return nil, nil
	}
	s.setLastAcked(p)
	return p, nil
}

// ComparePositions orders two positions using the configured comparator.
func (s *Source) ComparePositions(a, b laredo.Position) int {
	as, _ := a.(string)
	bs, _ := b.(string)
	return s.cfg.cmp(as, bs)
}

// PositionToString serializes a position (positions are already strings).
func (s *Source) PositionToString(p laredo.Position) string { ps, _ := p.(string); return ps }

// PositionFromString deserializes a position (positions are already strings).
func (s *Source) PositionFromString(str string) (laredo.Position, error) { return str, nil }

// Pause holds streaming until Resume; Stream forwards nothing while paused.
func (s *Source) Pause(_ context.Context) error {
	s.setState(laredo.SourcePaused)
	s.wake()
	return nil
}

// Resume returns the source to streaming after a Pause and wakes Stream.
func (s *Source) Resume(_ context.Context) error {
	s.setState(laredo.SourceStreaming)
	s.wake()
	return nil
}

// GetLag reports how far behind the archive head's timestamp the wall clock is.
func (s *Source) GetLag() laredo.LagInfo {
	s.mu.Lock()
	headTime := s.headTime
	s.mu.Unlock()
	if headTime.IsZero() {
		return laredo.LagInfo{}
	}
	d := time.Since(headTime)
	return laredo.LagInfo{LagTime: &d}
}

// OrderingGuarantee returns the configured ordering guarantee (default TotalOrder).
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return s.cfg.ordering }

// State returns the current source state.
func (s *Source) State() laredo.SourceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Close shuts the source down, waking a following Stream so it returns.
func (s *Source) Close(_ context.Context) error {
	s.setState(laredo.SourceClosed)
	s.wake()
	return nil
}

// waitPaused blocks until a state change (Resume/Close) wakes the source or the
// context is cancelled — no poll timer, since a paused source has nothing to do.
func (s *Source) waitPaused(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.signal:
		return nil
	}
}

// waitPoll blocks until the poll interval elapses, a state change wakes the
// source, or the context is cancelled.
func (s *Source) waitPoll(ctx context.Context) error {
	timer := time.NewTimer(s.cfg.pollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.signal:
		return nil
	case <-timer.C:
		return nil
	}
}

func (s *Source) setState(st laredo.SourceState) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

func (s *Source) setLastAcked(p string) {
	s.mu.Lock()
	s.lastAcked = p
	s.mu.Unlock()
}

func (s *Source) rememberHead(m *snapshotter.Manifest) {
	s.mu.Lock()
	s.headTime = m.UpdatedAt
	s.mu.Unlock()
}

// wake delivers a non-blocking nudge to a waiting Stream loop.
func (s *Source) wake() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// fromPosition returns a diff artifact's FromPosition for error messages ("" for
// a base, though diffs always carry one).
func fromPosition(a snapshotter.Artifact) string {
	if a.FromPosition == nil {
		return ""
	}
	return *a.FromPosition
}
