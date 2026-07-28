package sourcesub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
	"github.com/zourzouvillys/laredo/source/testsource"
)

func seedSource(t *testing.T) (*testsource.Source, laredo.TableIdentifier) {
	t.Helper()
	tbl := laredo.Table("public", "events")
	ts := testsource.New()
	ts.SetSchema(tbl, []laredo.ColumnDefinition{
		{Name: "id", Type: "int8", PrimaryKey: true, PrimaryKeyOrdinal: 1, OrdinalPosition: 1},
		{Name: "name", Type: "text", Nullable: true, OrdinalPosition: 2},
	})
	ts.AddRow(tbl, laredo.Row{"id": 1, "name": "alice"})
	return ts, tbl
}

// changeRecorder captures OnChange callbacks.
type changeRecorder struct {
	mu   sync.Mutex
	rows [][2]laredo.Row // {old, new}
}

func (r *changeRecorder) fn(old, new laredo.Row) {
	r.mu.Lock()
	r.rows = append(r.rows, [2]laredo.Row{old, new})
	r.mu.Unlock()
}

func (r *changeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

// TestAdapter_BaselineSnapshotColumns verifies Start captures the baseline and
// that Snapshot and Columns report it.
func TestAdapter_BaselineSnapshotColumns(t *testing.T) {
	ts, tbl := seedSource(t)
	a := New(ts, tbl, []string{"id"})
	defer a.Stop()

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !a.AwaitReady(time.Second) {
		t.Fatal("not ready")
	}
	rows, pos := a.Snapshot()
	if len(rows) != 1 || rows[0]["name"] != "alice" {
		t.Fatalf("snapshot: got %v", rows)
	}
	if pos == "" {
		t.Error("expected a non-empty position")
	}
	if a.Count() != 1 {
		t.Errorf("count: got %d, want 1", a.Count())
	}
	cols := a.Columns()
	if len(cols) != 2 || cols[0].Name != "id" || !cols[0].PrimaryKey {
		t.Errorf("columns: got %+v", cols)
	}
}

// TestAdapter_StreamAppliesAndForwards verifies changes fold into the state and
// reach the OnChange callback.
func TestAdapter_StreamAppliesAndForwards(t *testing.T) {
	ts, tbl := seedSource(t)
	a := New(ts, tbl, []string{"id"})
	defer a.Stop()

	rec := &changeRecorder{}
	a.OnChange(rec.fn)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	a.AwaitReady(time.Second)

	ts.EmitInsert(tbl, laredo.Row{"id": 2, "name": "bob"})
	ts.EmitUpdate(tbl, laredo.Row{"id": 1, "name": "alice2"}, laredo.Row{"id": 1, "name": "alice"})
	ts.EmitDelete(tbl, laredo.Row{"id": 2, "name": "bob"})

	waitFor(t, func() bool { return rec.count() >= 3 }, time.Second)

	// Final state: only the updated alice remains (bob inserted then deleted).
	rows, _ := a.Snapshot()
	if len(rows) != 1 || rows[0]["name"] != "alice2" {
		t.Fatalf("state after changes: got %v", rows)
	}
}

// TestAdapter_WriterEndToEnd runs a real Writer driven by the adapter and reads
// the archive back, proving continuous export records the schema.
func TestAdapter_WriterEndToEnd(t *testing.T) {
	ts, tbl := seedSource(t)
	a := New(ts, tbl, []string{"id"})

	dir := t.TempDir()
	const prefix = "public.events/"
	w, err := snapshotter.New(a, snapshotter.Config{
		Table:           "public.events",
		KeyPrefix:       prefix,
		Policy:          snapshotter.Policy{DiffInterval: 20 * time.Millisecond},
		SnapshotFormats: []snapshotter.Format{jsonl.New()},
		DiffFormats:     []snapshotter.Format{jsonl.New()},
		Destinations:    []snapshotter.Destination{local.New(dir)},
		KeyFields:       []string{"id"},
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- w.Run(ctx) }()

	reader, err := snapshotter.NewReader(local.New(dir), prefix, jsonl.New())
	if err != nil {
		t.Fatalf("reader: %v", err)
	}

	// Initial base snapshot lands.
	waitFor(t, func() bool { return artifactCount(reader) >= 1 }, 2*time.Second)

	// A change flushes as a diff.
	ts.EmitInsert(tbl, laredo.Row{"id": 2, "name": "bob"})
	waitFor(t, func() bool { return artifactCount(reader) >= 2 }, 2*time.Second)

	m, err := reader.LoadManifest(context.Background())
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if len(m.Columns) != 2 || m.Columns[0].Name != "id" || !m.Columns[0].PrimaryKey {
		t.Errorf("continuous archive should record schema, got %+v", m.Columns)
	}

	// The reconstructed state includes the streamed insert.
	rec, err := reader.ReconstructAsOf(context.Background(), m.HeadPosition, []string{"id"}, func(_, _ string) int { return 0 })
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if rec == nil || len(rec.Rows) != 2 {
		t.Fatalf("expected 2 reconstructed rows, got %v", rec)
	}

	cancel()
	<-runErr
}

// TestAdapter_StopBeforeStart verifies Stop is safe before Start.
func TestAdapter_StopBeforeStart(t *testing.T) {
	ts, tbl := seedSource(t)
	New(ts, tbl, nil).Stop() // must not panic or block
}

func waitFor(t *testing.T, cond func() bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func artifactCount(r *snapshotter.Reader) int {
	m, err := r.LoadManifest(context.Background())
	if err != nil {
		return 0
	}
	return len(m.Artifacts)
}
