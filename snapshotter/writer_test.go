package snapshotter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

// --- fake subscription ---------------------------------------------------

type fakeSub struct {
	mu       sync.Mutex
	rows     map[string]laredo.Row
	pos      string
	seq      int
	onChange func(old, new laredo.Row)
}

func newFakeSub() *fakeSub { return &fakeSub{rows: map[string]laredo.Row{}, pos: "0/0"} }

func (f *fakeSub) Start(context.Context) error       { return nil }
func (f *fakeSub) AwaitReady(time.Duration) bool     { return true }
func (f *fakeSub) CurrentPosition() string           { f.mu.Lock(); defer f.mu.Unlock(); return f.pos }
func (f *fakeSub) OnChange(fn func(o, n laredo.Row)) { f.onChange = fn }
func (f *fakeSub) Stop()                             {}
func (f *fakeSub) Count() int                        { f.mu.Lock(); defer f.mu.Unlock(); return len(f.rows) }

func (f *fakeSub) Snapshot() ([]laredo.Row, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]laredo.Row, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out, f.pos
}

func (f *fakeSub) put(id, name string) {
	f.mu.Lock()
	old := f.rows[id]
	row := laredo.Row{"id": id, "name": name}
	f.rows[id] = row
	f.seq++
	f.pos = fmt.Sprintf("0/%d", f.seq)
	cb := f.onChange
	f.mu.Unlock()
	if cb != nil {
		cb(old, row)
	}
}

func (f *fakeSub) del(id string) {
	f.mu.Lock()
	old := f.rows[id]
	delete(f.rows, id)
	f.seq++
	f.pos = fmt.Sprintf("0/%d", f.seq)
	cb := f.onChange
	f.mu.Unlock()
	if cb != nil {
		cb(old, nil)
	}
}

// --- fake in-memory destination -----------------------------------------

type memDest struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func newMemDest() *memDest { return &memDest{objs: map[string][]byte{}} }

func (d *memDest) Name() string { return "mem" }

func (d *memDest) Put(_ context.Context, key string, body io.Reader) (string, int64, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", 0, err
	}
	d.mu.Lock()
	d.objs[key] = data
	d.mu.Unlock()
	return "mem://" + key, int64(len(data)), nil
}

func (d *memDest) Get(_ context.Context, key string) ([]byte, string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, ok := d.objs[key]
	if !ok {
		return nil, "", false, nil
	}
	return data, revOf(data), true, nil
}

func (d *memDest) PutCAS(_ context.Context, key string, body []byte, expectedRev string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := ""
	if data, ok := d.objs[key]; ok {
		cur = revOf(data)
	}
	if cur != expectedRev {
		return "", ErrCASConflict
	}
	d.objs[key] = body
	return revOf(body), nil
}

func revOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// jsonFormat is a minimal in-test Format (importing the jsonl subpackage would
// create an import cycle with this white-box test).
type jsonFormat struct{}

func (jsonFormat) FormatID() string  { return "testjson" }
func (jsonFormat) Extension() string { return ".json" }
func (jsonFormat) WriteSnapshot(w io.Writer, r []laredo.Row) error {
	return json.NewEncoder(w).Encode(r)
}
func (jsonFormat) WriteDiff(w io.Writer, c []Change) error { return json.NewEncoder(w).Encode(c) }

func (jsonFormat) ReadSnapshot(r io.Reader) ([]laredo.Row, error) {
	var rows []laredo.Row
	err := json.NewDecoder(r).Decode(&rows)
	return rows, err
}

func (jsonFormat) ReadDiff(r io.Reader) ([]Change, error) {
	var ch []Change
	err := json.NewDecoder(r).Decode(&ch)
	return ch, err
}

// --- tests ---------------------------------------------------------------

func newTestWriter(t *testing.T, sub Subscription, policy Policy, clock func() time.Time) (*Writer, *memDest) {
	t.Helper()
	d := newMemDest()
	w, err := New(sub, Config{
		Table:           "public.users",
		KeyPrefix:       "users/",
		Policy:          policy,
		SnapshotFormats: []Format{jsonFormat{}},
		DiffFormats:     []Format{jsonFormat{}},
		Destinations:    []Destination{d},
		Now:             clock,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w, d
}

func TestWriter_SnapshotThenDiffThenRebase(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	clock := func() time.Time { return now }

	sub := newFakeSub()
	sub.put("1", "alice")
	sub.put("2", "bob")

	w, d := newTestWriter(t, sub, Policy{DiffInterval: time.Hour, MaxChurnRecords: 3}, clock)
	w.sub.OnChange(w.onChange)
	if err := w.loadManifest(ctx); err != nil {
		t.Fatalf("loadManifest: %v", err)
	}

	if err := w.snapshot(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	m := readManifest(t, d)
	if m.Epoch != 1 || len(m.Artifacts) != 1 || m.Artifacts[0].Kind != KindSnapshot || m.Artifacts[0].RowCount != 2 {
		t.Fatalf("after initial snapshot: %+v", m)
	}

	sub.put("3", "carol")
	if err := w.flush(ctx); err != nil {
		t.Fatalf("flush diff: %v", err)
	}
	m = readManifest(t, d)
	if len(m.Artifacts) != 2 || m.Artifacts[1].Kind != KindDiff || m.Epoch != 1 {
		t.Fatalf("after diff: %+v", m)
	}
	assertReconstruct(t, d, sub)

	// Churn past the threshold -> the next flush re-bases instead of diffing.
	sub.put("1", "alice2")
	sub.del("2")
	sub.put("4", "dave")
	if err := w.flush(ctx); err != nil {
		t.Fatalf("flush rebase: %v", err)
	}
	m = readManifest(t, d)
	if m.Epoch != 2 {
		t.Fatalf("expected re-base to epoch 2, got %+v", m)
	}
	if latest := m.latestSnapshot(); latest == nil || latest.Epoch != 2 {
		t.Fatalf("latest snapshot not epoch 2: %+v", latest)
	}
	assertReconstruct(t, d, sub)
}

func TestWriter_ForceSnapshotViaRun(t *testing.T) {
	sub := newFakeSub()
	sub.put("1", "alice")

	w, d := newTestWriter(t, sub, Policy{DiffInterval: time.Hour}, time.Now)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitFor(t, func() bool { return readManifestIfPresent(d) != nil })

	if err := w.Snapshot(ctx); err != nil {
		t.Fatalf("force snapshot: %v", err)
	}
	if m := readManifest(t, d); m.Epoch < 2 {
		t.Fatalf("forced snapshot did not bump epoch: %+v", m)
	}

	cancel()
	<-done
}

func TestWriter_CASConflictAcrossTwoWriters(t *testing.T) {
	ctx := context.Background()
	d := newMemDest()
	mk := func() *Writer {
		w, err := New(newFakeSub(), Config{
			Table:           "public.users",
			KeyPrefix:       "users/",
			Policy:          Policy{DiffInterval: time.Hour},
			SnapshotFormats: []Format{jsonFormat{}},
			DiffFormats:     []Format{jsonFormat{}},
			Destinations:    []Destination{d},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return w
	}
	w1, w2 := mk(), mk()
	if err := w1.loadManifest(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w2.loadManifest(ctx); err != nil {
		t.Fatal(err)
	}
	// w1 commits first.
	if err := w1.snapshot(ctx); err != nil {
		t.Fatalf("w1 snapshot: %v", err)
	}
	// w2 holds a stale manifest rev -> its commit must conflict.
	err := w2.snapshot(ctx)
	if err == nil {
		t.Fatal("expected w2 commit to conflict with w1")
	}
}

// --- helpers -------------------------------------------------------------

func readManifest(t *testing.T, d *memDest) Manifest {
	t.Helper()
	m := readManifestIfPresent(d)
	if m == nil {
		t.Fatal("manifest not found")
	}
	return *m
}

func readManifestIfPresent(d *memDest) *Manifest {
	d.mu.Lock()
	data, ok := d.objs["users/manifest.json"]
	d.mu.Unlock()
	if !ok {
		return nil
	}
	var m Manifest
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

func assertReconstruct(t *testing.T, d *memDest, sub *fakeSub) {
	t.Helper()
	m := readManifest(t, d)
	snap := m.latestSnapshot()
	if snap == nil {
		t.Fatal("no snapshot to reconstruct from")
	}
	f := jsonFormat{}
	state := map[string]laredo.Row{}
	rows, err := f.ReadSnapshot(bytes.NewReader(artifactBytes(t, d, snap)))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	for _, r := range rows {
		state[fmt.Sprintf("%v", r["id"])] = r
	}
	for i := range m.Artifacts {
		a := &m.Artifacts[i]
		if a.Kind != KindDiff || a.Epoch != snap.Epoch {
			continue
		}
		changes, err := f.ReadDiff(bytes.NewReader(artifactBytes(t, d, a)))
		if err != nil {
			t.Fatalf("read diff: %v", err)
		}
		for _, c := range changes {
			switch c.Action {
			case laredo.ActionInsert, laredo.ActionUpdate:
				state[c.Key] = c.New
			case laredo.ActionDelete:
				delete(state, c.Key)
			}
		}
	}

	want, _ := sub.Snapshot()
	if len(state) != len(want) {
		t.Fatalf("reconstruct size %d, want %d", len(state), len(want))
	}
	for _, r := range want {
		k := fmt.Sprintf("%v", r["id"])
		if fmt.Sprintf("%v", state[k]) != fmt.Sprintf("%v", r) {
			t.Fatalf("reconstruct[%s] = %v, want %v", k, state[k], r)
		}
	}
}

// artifactBytes fetches one artifact's testjson bytes from the destination.
func artifactBytes(t *testing.T, d *memDest, a *Artifact) []byte {
	t.Helper()
	ref, ok := a.Formats["testjson"]
	if !ok {
		t.Fatalf("artifact missing testjson format: %+v", a)
	}
	key := ref.URI[len("mem://"):]
	d.mu.Lock()
	data, ok := d.objs[key]
	d.mu.Unlock()
	if !ok {
		t.Fatalf("artifact object %s not found", key)
	}
	return data
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
