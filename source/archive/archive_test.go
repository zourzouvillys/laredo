package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
)

const (
	testPrefix = "public.events/"
	bobName    = "bob"
)

// --- test harness ---------------------------------------------------------

// archiveWriter writes snapshotter artifacts and a manifest to a local
// destination, mirroring the on-disk layout the snapshotter Writer produces.
type archiveWriter struct {
	t      *testing.T
	dest   *local.Destination
	f      snapshotter.Format
	prefix string
	arts   []snapshotter.Artifact
}

func newArchiveWriter(t *testing.T, dir, prefix string) *archiveWriter {
	return &archiveWriter{t: t, dest: local.New(dir), f: jsonl.New(), prefix: prefix}
}

func (w *archiveWriter) put(art snapshotter.Artifact, payload []byte) {
	w.t.Helper()
	key := snapshotter.ArtifactObjectKey(w.prefix, art, w.f.Extension())
	if _, _, err := w.dest.Put(context.Background(), key, bytes.NewReader(payload)); err != nil {
		w.t.Fatalf("put artifact %s: %v", key, err)
	}
}

func (w *archiveWriter) snapshot(epoch int64, pos string, rows []laredo.Row) {
	w.t.Helper()
	art := snapshotter.Artifact{
		Kind: snapshotter.KindSnapshot, Epoch: epoch, ToPosition: pos,
		RowCount: int64(len(rows)), Formats: map[string]snapshotter.FormatRef{"jsonl": {}},
	}
	var b bytes.Buffer
	if err := w.f.WriteSnapshot(&b, rows); err != nil {
		w.t.Fatalf("write snapshot: %v", err)
	}
	w.put(art, b.Bytes())
	w.arts = append(w.arts, art)
}

func (w *archiveWriter) diff(epoch int64, from, to string, changes []snapshotter.Change) {
	w.t.Helper()
	fp := from
	art := snapshotter.Artifact{
		Kind: snapshotter.KindDiff, Epoch: epoch, FromPosition: &fp, ToPosition: to,
		ChangeCount: int64(len(changes)), Formats: map[string]snapshotter.FormatRef{"jsonl": {}},
	}
	var b bytes.Buffer
	if err := w.f.WriteDiff(&b, changes); err != nil {
		w.t.Fatalf("write diff: %v", err)
	}
	w.put(art, b.Bytes())
	w.arts = append(w.arts, art)
}

// commit writes the manifest referencing the artifacts written so far.
func (w *archiveWriter) commit(epoch int64, head string, cols []laredo.ColumnDefinition) {
	w.t.Helper()
	m := snapshotter.Manifest{
		ManifestVersion: snapshotter.ManifestVersion,
		Table:           "public.events",
		Epoch:           epoch,
		UpdatedAt:       time.Now(),
		HeadPosition:    head,
		Artifacts:       append([]snapshotter.Artifact(nil), w.arts...),
		Columns:         cols,
	}
	data, err := json.Marshal(m)
	if err != nil {
		w.t.Fatalf("marshal manifest: %v", err)
	}
	if _, _, err := w.dest.Put(context.Background(), snapshotter.ManifestObjectKey(w.prefix), bytes.NewReader(data)); err != nil {
		w.t.Fatalf("put manifest: %v", err)
	}
}

// reset drops the accumulated artifact list, for a wholesale archive replacement
// (a new epoch whose manifest references only fresh artifacts).
func (w *archiveWriter) reset() { w.arts = nil }

func (w *archiveWriter) reader() *snapshotter.Reader {
	w.t.Helper()
	r, err := snapshotter.NewReader(w.dest, w.prefix, w.f)
	if err != nil {
		w.t.Fatalf("new reader: %v", err)
	}
	return r
}

// collector captures change events emitted by Stream.
type collector struct {
	mu     sync.Mutex
	events []laredo.ChangeEvent
}

func (c *collector) OnChange(e laredo.ChangeEvent) error {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
	return nil
}

func (c *collector) all() []laredo.ChangeEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]laredo.ChangeEvent(nil), c.events...)
}

func (c *collector) waitFor(t *testing.T, n int, within time.Duration) []laredo.ChangeEvent {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if evs := c.all(); len(evs) >= n {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d events, got %d", n, len(c.all()))
	return nil
}

func baseSource(t *testing.T, w *archiveWriter, opts ...Option) *Source {
	t.Helper()
	all := append([]Option{
		WithReader(w.reader()),
		Table("public", "events"),
		KeyFields("id"),
	}, opts...)
	s := New(all...)
	if _, err := s.Init(context.Background(), laredo.SourceConfig{}); err != nil {
		t.Fatalf("init: %v", err)
	}
	return s
}

func doBaseline(t *testing.T, s *Source) ([]laredo.Row, laredo.Position) {
	t.Helper()
	var rows []laredo.Row
	pos, err := s.Baseline(context.Background(), nil, func(_ laredo.TableIdentifier, r laredo.Row) {
		rows = append(rows, r)
	})
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	return rows, pos
}

// --- tests ----------------------------------------------------------------

// TestBaselineFoldsCurrentState verifies Baseline reconstructs the folded
// current state at the head — base snapshot plus applied diffs — rather than
// replaying history as change events.
func TestBaselineFoldsCurrentState(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.diff(1, "0/1", "0/2", []snapshotter.Change{{Action: laredo.ActionInsert, Key: "2", New: laredo.Row{"id": 2, "name": bobName}}})
	w.commit(1, "0/2", nil)

	s := baseSource(t, w)
	rows, pos := doBaseline(t, s)
	if len(rows) != 2 {
		t.Fatalf("baseline rows: got %d, want 2 (%v)", len(rows), rows)
	}
	if pos != "0/2" {
		t.Errorf("baseline position: got %v, want 0/2", pos)
	}

	// One-shot Stream from the head returns immediately with no events.
	c := &collector{}
	if err := s.Stream(context.Background(), pos, c); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := c.all(); len(got) != 0 {
		t.Errorf("expected no stream events at head, got %d", len(got))
	}
}

// TestStreamEmitsDiffsFromPosition drives Stream from a position before the head
// (the resume shape) and asserts each diff is emitted as a change event.
func TestStreamEmitsDiffsFromPosition(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.diff(1, "0/1", "0/2", []snapshotter.Change{{Action: laredo.ActionInsert, Key: "2", New: laredo.Row{"id": 2, "name": bobName}}})
	w.diff(1, "0/2", "0/3", []snapshotter.Change{{Action: laredo.ActionUpdate, Key: "1", Old: laredo.Row{"id": 1, "name": "alice"}, New: laredo.Row{"id": 1, "name": "alice2"}}})
	w.commit(1, "0/3", nil)

	s := baseSource(t, w)
	c := &collector{}
	if err := s.Stream(context.Background(), "0/1", c); err != nil {
		t.Fatalf("stream: %v", err)
	}
	evs := c.all()
	if len(evs) != 2 {
		t.Fatalf("expected 2 events, got %d (%v)", len(evs), evs)
	}
	if evs[0].Action != laredo.ActionInsert || evs[0].Position != "0/2" {
		t.Errorf("event0: got %+v", evs[0])
	}
	if evs[1].Action != laredo.ActionUpdate || evs[1].Position != "0/3" {
		t.Errorf("event1: got %+v", evs[1])
	}
	if evs[1].NewValues["name"] != "alice2" {
		t.Errorf("update new value: got %v", evs[1].NewValues)
	}
}

// TestStreamTruncate verifies a truncate diff maps to a truncate change event
// carrying no row values.
func TestStreamTruncate(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.diff(1, "0/1", "0/2", []snapshotter.Change{{Action: laredo.ActionTruncate}})
	w.commit(1, "0/2", nil)

	s := baseSource(t, w)
	c := &collector{}
	if err := s.Stream(context.Background(), "0/1", c); err != nil {
		t.Fatalf("stream: %v", err)
	}
	evs := c.all()
	if len(evs) != 1 || evs[0].Action != laredo.ActionTruncate {
		t.Fatalf("expected 1 truncate event, got %v", evs)
	}
	if evs[0].NewValues != nil || evs[0].OldValues != nil {
		t.Errorf("truncate should carry no values, got %+v", evs[0])
	}
}

// TestFollowAppend verifies follow mode picks up a diff appended after the
// consumer has caught up to the head.
func TestFollowAppend(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.commit(1, "0/1", nil)

	s := baseSource(t, w, Follow(true), PollInterval(10*time.Millisecond))
	_, pos := doBaseline(t, s)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &collector{}
	errCh := make(chan error, 1)
	go func() { errCh <- s.Stream(ctx, pos, c) }()

	// Append a diff after the consumer is following.
	time.Sleep(20 * time.Millisecond)
	w.diff(1, "0/1", "0/2", []snapshotter.Change{{Action: laredo.ActionInsert, Key: "2", New: laredo.Row{"id": 2, "name": bobName}}})
	w.commit(1, "0/2", nil)

	evs := c.waitFor(t, 1, time.Second)
	if evs[0].Action != laredo.ActionInsert || evs[0].NewValues["name"] != bobName {
		t.Errorf("expected insert bob, got %+v", evs[0])
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Errorf("stream should return context.Canceled, got %v", err)
	}
}

// TestCompleteReplacementRebaselines verifies that when the archive is replaced
// wholesale (a new epoch whose base no longer continues the consumer's position)
// the follower returns ErrReBaselineRequired.
func TestCompleteReplacementRebaselines(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.diff(1, "0/1", "0/2", []snapshotter.Change{{Action: laredo.ActionInsert, Key: "2", New: laredo.Row{"id": 2, "name": bobName}}})
	w.commit(1, "0/2", nil)

	s := baseSource(t, w, Follow(true), PollInterval(10*time.Millisecond))
	_, pos := doBaseline(t, s)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Stream(ctx, pos, &collector{}) }()

	// Replace the archive: a fresh epoch with an unrelated base position, no diff
	// chain continuing "0/2".
	time.Sleep(20 * time.Millisecond)
	w.reset()
	w.snapshot(2, "5/0", []laredo.Row{{"id": 9, "name": "zoe"}})
	w.commit(2, "5/0", nil)

	select {
	case err := <-errCh:
		if !errors.Is(err, laredo.ErrReBaselineRequired) {
			t.Fatalf("expected ErrReBaselineRequired, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for re-baseline signal")
	}

	// A fresh baseline reflects the replacement.
	rows, newPos := doBaseline(t, s)
	if newPos != "5/0" || len(rows) != 1 || rows[0]["name"] != "zoe" {
		t.Errorf("re-baseline: pos=%v rows=%v", newPos, rows)
	}
}

// TestResumeAcrossRestart verifies the state file lets a new Source instance
// continue from the last ACKed position instead of re-baselining.
func TestResumeAcrossRestart(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	statePath := t.TempDir() + "/ack.pos"
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.commit(1, "0/1", nil)

	// First instance: baseline, ack the head, close.
	s1 := baseSource(t, w, StatePath(statePath))
	if !s1.SupportsResume() {
		t.Fatal("SupportsResume should be true when a state path is set")
	}
	_, pos := doBaseline(t, s1)
	if err := s1.Ack(context.Background(), pos); err != nil {
		t.Fatalf("ack: %v", err)
	}
	_ = s1.Close(context.Background())

	// Archive grows while the source is down.
	w.diff(1, "0/1", "0/2", []snapshotter.Change{{Action: laredo.ActionInsert, Key: "2", New: laredo.Row{"id": 2, "name": bobName}}})
	w.commit(1, "0/2", nil)

	// Second instance resumes from the persisted position.
	s2 := baseSource(t, w, StatePath(statePath))
	last, err := s2.LastAckedPosition(context.Background())
	if err != nil {
		t.Fatalf("last acked: %v", err)
	}
	if last != "0/1" {
		t.Fatalf("resume position: got %v, want 0/1", last)
	}
	c := &collector{}
	if err := s2.Stream(context.Background(), last, c); err != nil {
		t.Fatalf("stream: %v", err)
	}
	evs := c.all()
	if len(evs) != 1 || evs[0].NewValues["name"] != bobName {
		t.Fatalf("expected the missed insert bob, got %v", evs)
	}
}

// TestSchemaFromManifest verifies Init returns the manifest's recorded columns
// verbatim when present.
func TestSchemaFromManifest(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	cols := []laredo.ColumnDefinition{
		{Name: "id", Type: "int8", PrimaryKey: true, PrimaryKeyOrdinal: 1, OrdinalPosition: 1},
		{Name: "name", Type: "text", Nullable: true, OrdinalPosition: 2},
	}
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.commit(1, "0/1", cols)

	s := New(WithReader(w.reader()), Table("public", "events"), KeyFields("id"))
	got, err := s.Init(context.Background(), laredo.SourceConfig{})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	tbl := laredo.Table("public", "events")
	if len(got[tbl]) != 2 || got[tbl][0].Type != "int8" || !got[tbl][0].PrimaryKey {
		t.Fatalf("expected recorded schema, got %+v", got[tbl])
	}
}

// TestSchemaInferred verifies Init infers column names from a snapshot row when
// the manifest carries no schema, marking the configured key field as primary.
func TestSchemaInferred(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.commit(1, "0/1", nil)

	s := New(WithReader(w.reader()), Table("public", "events"), KeyFields("id"))
	got, err := s.Init(context.Background(), laredo.SourceConfig{})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	cols := got[laredo.Table("public", "events")]
	if len(cols) != 2 {
		t.Fatalf("expected 2 inferred columns, got %+v", cols)
	}
	// Names are sorted: id, name.
	if cols[0].Name != "id" || cols[1].Name != "name" {
		t.Errorf("column order: got %v, %v", cols[0].Name, cols[1].Name)
	}
	if !cols[0].PrimaryKey || cols[1].PrimaryKey {
		t.Errorf("only id should be a primary key: %+v", cols)
	}
}

func TestInferColumnsEmpty(t *testing.T) {
	if got := inferColumns(nil, []string{"id"}); got != nil {
		t.Errorf("inferColumns(nil) = %v, want nil", got)
	}
}

// TestValidateTables checks the source rejects tables it does not serve.
func TestValidateTables(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1}})
	w.commit(1, "0/1", nil)
	s := New(WithReader(w.reader()), Table("public", "events"))

	ok := s.ValidateTables(context.Background(), []laredo.TableIdentifier{laredo.Table("public", "events")})
	if len(ok) != 0 {
		t.Errorf("served table should validate, got %v", ok)
	}
	bad := s.ValidateTables(context.Background(), []laredo.TableIdentifier{laredo.Table("public", "other")})
	if len(bad) != 1 || bad[0].Code != "TABLE_NOT_SERVED" {
		t.Errorf("unserved table should error, got %v", bad)
	}
}

// TestPositionAndOrdering covers the position round-trip, comparator, and the
// reported ordering guarantee.
func TestPositionAndOrdering(t *testing.T) {
	s := New(Table("public", "events"))
	if s.PositionToString("0/2A") != "0/2A" {
		t.Errorf("PositionToString round-trip failed")
	}
	p, err := s.PositionFromString("0/2A")
	if err != nil || p != "0/2A" {
		t.Errorf("PositionFromString: %v %v", p, err)
	}
	if s.ComparePositions("0/1", "0/2") >= 0 {
		t.Errorf("0/1 should sort before 0/2")
	}
	if s.OrderingGuarantee() != laredo.TotalOrder {
		t.Errorf("default ordering should be TotalOrder")
	}
	if s.SupportsResume() {
		t.Errorf("SupportsResume should be false without a state path")
	}
}

// TestTableDerivedFromInit verifies a source built without an explicit Table
// option adopts the single table the engine binds to it.
func TestTableDerivedFromInit(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1}})
	w.commit(1, "0/1", nil)

	s := New(WithReader(w.reader()), KeyFields("id")) // no Table option
	tbl := laredo.Table("public", "events")
	cols, err := s.Init(context.Background(), laredo.SourceConfig{Tables: []laredo.TableIdentifier{tbl}})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, ok := cols[tbl]; !ok {
		t.Fatalf("expected schema for %s, got %v", tbl, cols)
	}
	if errs := s.ValidateTables(context.Background(), []laredo.TableIdentifier{tbl}); len(errs) != 0 {
		t.Errorf("derived table should validate, got %v", errs)
	}
}

// TestTableRequiresExactlyOne verifies Init rejects a binding that is not exactly
// one table (a manifest is per-table).
func TestTableRequiresExactlyOne(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1}})
	w.commit(1, "0/1", nil)

	s := New(WithReader(w.reader()))
	_, err := s.Init(context.Background(), laredo.SourceConfig{Tables: []laredo.TableIdentifier{
		laredo.Table("public", "a"), laredo.Table("public", "b"),
	}})
	if err == nil {
		t.Fatal("expected error for multiple bound tables")
	}
}

// TestPauseResume verifies Pause holds streaming (nothing is emitted) and Resume
// flushes changes appended while paused, exercising GetLag and the paused wait.
func TestPauseResume(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})
	w.commit(1, "0/1", nil)

	s := baseSource(t, w, Follow(true), PollInterval(10*time.Millisecond))
	_, pos := doBaseline(t, s)
	if lag := s.GetLag(); lag.LagTime == nil {
		t.Error("expected a lag time once the head timestamp is known")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &collector{}
	errCh := make(chan error, 1)
	go func() { errCh <- s.Stream(ctx, pos, c) }()

	time.Sleep(20 * time.Millisecond) // let Stream reach the head and start following
	if err := s.Pause(ctx); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if s.State() != laredo.SourcePaused {
		t.Errorf("state after pause: got %v", s.State())
	}
	time.Sleep(20 * time.Millisecond) // ensure the loop is parked in the paused wait

	w.diff(1, "0/1", "0/2", []snapshotter.Change{{Action: laredo.ActionInsert, Key: "2", New: laredo.Row{"id": 2, "name": bobName}}})
	w.commit(1, "0/2", nil)
	time.Sleep(30 * time.Millisecond)
	if got := c.all(); len(got) != 0 {
		t.Fatalf("a paused source must emit nothing, got %d", len(got))
	}

	if err := s.Resume(ctx); err != nil {
		t.Fatalf("resume: %v", err)
	}
	evs := c.waitFor(t, 1, time.Second)
	if evs[0].NewValues["name"] != bobName {
		t.Errorf("expected bob after resume, got %+v", evs[0])
	}
	cancel()
	<-errCh
}

// TestBaselineEmptyArchive verifies an archive with a manifest but no base
// snapshot yields an empty baseline and no error (Init and Baseline degrade).
func TestBaselineEmptyArchive(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.commit(1, "", nil) // manifest with no artifacts

	s := New(WithReader(w.reader()), Table("public", "events"))
	if _, err := s.Init(context.Background(), laredo.SourceConfig{}); err != nil {
		t.Fatalf("init on empty archive: %v", err)
	}
	rows, pos := doBaseline(t, s)
	if len(rows) != 0 {
		t.Errorf("empty baseline should emit no rows, got %d", len(rows))
	}
	if pos != "" {
		t.Errorf("empty baseline position: got %v, want empty", pos)
	}
}

// TestStreamReadDiffError verifies Stream surfaces an error when a manifest
// references a diff whose object is missing.
func TestStreamReadDiffError(t *testing.T) {
	dir, prefix := t.TempDir(), testPrefix
	w := newArchiveWriter(t, dir, prefix)
	w.snapshot(1, "0/1", []laredo.Row{{"id": 1}})
	// Reference a diff in the manifest without writing its object.
	from := "0/1"
	w.arts = append(w.arts, snapshotter.Artifact{
		Kind: snapshotter.KindDiff, Epoch: 1, FromPosition: &from, ToPosition: "0/2",
		Formats: map[string]snapshotter.FormatRef{"jsonl": {}},
	})
	w.commit(1, "0/2", nil)

	s := baseSource(t, w)
	if err := s.Stream(context.Background(), "0/1", &collector{}); err == nil {
		t.Fatal("expected an error reading a missing diff object")
	}
}

// TestInitRequiresReader verifies Init fails clearly when no reader is set.
func TestInitRequiresReader(t *testing.T) {
	s := New(Table("public", "events"))
	if _, err := s.Init(context.Background(), laredo.SourceConfig{}); err == nil {
		t.Fatal("expected an error when no reader is configured")
	}
}
