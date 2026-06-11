package snapshotter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
)

const prefix = "public.events/"

// Repeated row-name values, named to satisfy goconst.
const (
	nameAlice = "alice"
	nameBob   = "bob"
	nameBob2  = "bob2"
	nameCarol = "carol"
)

// posCmp compares the integer-valued positions used in these tests.
func posCmp(a, b string) int {
	ai, _ := strconv.Atoi(a)
	bi, _ := strconv.Atoi(b)
	switch {
	case ai < bi:
		return -1
	case ai > bi:
		return 1
	default:
		return 0
	}
}

func strptr(s string) *string { return &s }

// newArchive writes a manifest plus its referenced artifacts to a fresh local
// destination and returns a Reader over it. Artifacts are written in the exact
// on-disk layout the Writer uses (ArtifactObjectKey), encoded with real JSONL.
func newArchive(t *testing.T, m snapshotter.Manifest, snaps map[int64][]laredo.Row, diffs map[string][]snapshotter.Change) *snapshotter.Reader {
	t.Helper()
	dir := t.TempDir()
	dest := local.New(dir)
	f := jsonl.New()
	ctx := context.Background()

	for _, art := range m.Artifacts {
		var buf bytes.Buffer
		switch art.Kind {
		case snapshotter.KindSnapshot:
			if err := f.WriteSnapshot(&buf, snaps[art.Epoch]); err != nil {
				t.Fatalf("write snapshot: %v", err)
			}
		case snapshotter.KindDiff:
			if err := f.WriteDiff(&buf, diffs[art.ToPosition]); err != nil {
				t.Fatalf("write diff: %v", err)
			}
		}
		key := snapshotter.ArtifactObjectKey(prefix, art, f.Extension())
		if _, _, err := dest.Put(ctx, key, bytes.NewReader(buf.Bytes())); err != nil {
			t.Fatalf("put artifact: %v", err)
		}
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if _, _, err := dest.Put(ctx, snapshotter.ManifestObjectKey(prefix), bytes.NewReader(data)); err != nil {
		t.Fatalf("put manifest: %v", err)
	}

	r, err := snapshotter.NewReader(dest, prefix, f)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	return r
}

// fmtRef is a Formats map carrying just the jsonl id (the reader derives the
// object key itself; URI/size are irrelevant to reading).
func fmtRef() map[string]snapshotter.FormatRef {
	return map[string]snapshotter.FormatRef{"jsonl": {}}
}

// chainManifest builds snapshot@100, diff(100→200), diff(200→300); head=300.
func chainManifest() snapshotter.Manifest {
	return snapshotter.Manifest{
		ManifestVersion: snapshotter.ManifestVersion,
		Table:           "public.events",
		Epoch:           1,
		HeadPosition:    "300",
		Artifacts: []snapshotter.Artifact{
			{Kind: snapshotter.KindSnapshot, Epoch: 1, ToPosition: "100", RowCount: 2, Formats: fmtRef()},
			{Kind: snapshotter.KindDiff, Epoch: 1, FromPosition: strptr("100"), ToPosition: "200", ChangeCount: 1, Formats: fmtRef()},
			{Kind: snapshotter.KindDiff, Epoch: 1, FromPosition: strptr("200"), ToPosition: "300", ChangeCount: 1, Formats: fmtRef()},
		},
	}
}

func TestReader_LoadManifest(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		dest := local.New(t.TempDir())
		r, _ := snapshotter.NewReader(dest, prefix, jsonl.New())
		_, err := r.LoadManifest(context.Background())
		if !errors.Is(err, snapshotter.ErrManifestNotFound) {
			t.Fatalf("err = %v, want ErrManifestNotFound", err)
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		m := chainManifest()
		m.ManifestVersion = snapshotter.ManifestVersion + 1
		r := newArchive(t, m, map[int64][]laredo.Row{1: {{"id": 1}, {"id": 2}}}, nil)
		_, err := r.LoadManifest(context.Background())
		if !errors.Is(err, snapshotter.ErrUnsupportedManifestVersion) {
			t.Fatalf("err = %v, want ErrUnsupportedManifestVersion", err)
		}
	})

	t.Run("ok", func(t *testing.T) {
		r := newArchive(t, chainManifest(), map[int64][]laredo.Row{1: {{"id": 1}, {"id": 2}}}, nil)
		m, err := r.LoadManifest(context.Background())
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if m.HeadPosition != "300" || len(m.Artifacts) != 3 {
			t.Fatalf("manifest = %+v", m)
		}
	})
}

func TestReader_ReadArtifacts(t *testing.T) {
	snapRows := []laredo.Row{{"id": float64(1), "name": nameAlice}, {"id": float64(2), "name": nameBob}}
	diff200 := []snapshotter.Change{{Action: laredo.ActionInsert, Key: "3", New: laredo.Row{"id": float64(3), "name": nameCarol}}}
	m := chainManifest()
	r := newArchive(t, m, map[int64][]laredo.Row{1: snapRows}, map[string][]snapshotter.Change{"200": diff200, "300": nil})

	ctx := context.Background()
	rows, err := r.ReadSnapshot(ctx, m.Artifacts[0])
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if len(rows) != 2 || rows[0].GetString("name") != nameAlice {
		t.Fatalf("snapshot rows = %v", rows)
	}

	changes, err := r.ReadDiff(ctx, m.Artifacts[1])
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if len(changes) != 1 || changes[0].Action != laredo.ActionInsert || changes[0].New.GetString("name") != nameCarol {
		t.Fatalf("diff changes = %v", changes)
	}

	t.Run("missing artifact object", func(t *testing.T) {
		bogus := snapshotter.Artifact{Kind: snapshotter.KindSnapshot, Epoch: 99, ToPosition: "999", Formats: fmtRef()}
		if _, err := r.ReadSnapshot(ctx, bogus); err == nil {
			t.Fatal("expected error for missing artifact object")
		}
	})

	t.Run("undecodable format", func(t *testing.T) {
		art := snapshotter.Artifact{Kind: snapshotter.KindSnapshot, Epoch: 1, ToPosition: "100", Formats: map[string]snapshotter.FormatRef{"protobuf": {}}}
		if _, err := r.ReadSnapshot(ctx, art); err == nil {
			t.Fatal("expected error when no configured format matches")
		}
	})
}

func TestReader_Plan(t *testing.T) {
	r := newArchive(t, chainManifest(), map[int64][]laredo.Row{1: {{"id": 1}}}, map[string][]snapshotter.Change{"200": nil, "300": nil})
	m, err := r.LoadManifest(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	t.Run("snapshot-base when position predates the chain", func(t *testing.T) {
		plan, err := r.Plan(m, "50", posCmp)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan == nil || plan.Snapshot == nil || plan.Snapshot.ToPosition != "100" {
			t.Fatalf("plan = %+v, want snapshot@100", plan)
		}
		if len(plan.Diffs) != 2 || plan.HeadPosition != "300" {
			t.Fatalf("plan diffs/head = %v / %s", plan.Diffs, plan.HeadPosition)
		}
	})

	t.Run("diff-only when position aligns to a diff boundary", func(t *testing.T) {
		plan, err := r.Plan(m, "100", posCmp)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan == nil || plan.Snapshot != nil {
			t.Fatalf("plan = %+v, want diff-only (no snapshot)", plan)
		}
		if len(plan.Diffs) != 2 || plan.HeadPosition != "300" {
			t.Fatalf("plan diffs/head = %v / %s", plan.Diffs, plan.HeadPosition)
		}
	})

	t.Run("diff-only from a later boundary", func(t *testing.T) {
		plan, err := r.Plan(m, "200", posCmp)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan == nil || plan.Snapshot != nil || len(plan.Diffs) != 1 || plan.Diffs[0].ToPosition != "300" {
			t.Fatalf("plan = %+v, want single diff 200→300", plan)
		}
	})

	t.Run("empty manifest yields no plan", func(t *testing.T) {
		empty := &snapshotter.Manifest{ManifestVersion: snapshotter.ManifestVersion}
		plan, err := r.Plan(empty, "0", posCmp)
		if err != nil || plan != nil {
			t.Fatalf("plan = %+v, err = %v; want nil, nil", plan, err)
		}
	})

	t.Run("diffs but no snapshot yields no plan", func(t *testing.T) {
		m2 := &snapshotter.Manifest{
			ManifestVersion: snapshotter.ManifestVersion,
			HeadPosition:    "300",
			Artifacts: []snapshotter.Artifact{
				{Kind: snapshotter.KindDiff, Epoch: 1, FromPosition: strptr("200"), ToPosition: "300", Formats: fmtRef()},
			},
		}
		plan, err := r.Plan(m2, "50", posCmp)
		if err != nil || plan != nil {
			t.Fatalf("plan = %+v, err = %v; want nil, nil (no base snapshot)", plan, err)
		}
	})
}

// asofArchive builds: snapshot@100 [alice(1), bob(2)]; diff(100→200) updates bob
// and inserts carol(3); diff(200→300) deletes alice(1). head = 300.
func asofArchive(t *testing.T) *snapshotter.Reader {
	t.Helper()
	m := snapshotter.Manifest{
		ManifestVersion: snapshotter.ManifestVersion,
		Table:           "public.events",
		Epoch:           1,
		HeadPosition:    "300",
		Artifacts: []snapshotter.Artifact{
			{Kind: snapshotter.KindSnapshot, Epoch: 1, ToPosition: "100", RowCount: 2, Formats: fmtRef()},
			{Kind: snapshotter.KindDiff, Epoch: 1, FromPosition: strptr("100"), ToPosition: "200", ChangeCount: 2, Formats: fmtRef()},
			{Kind: snapshotter.KindDiff, Epoch: 1, FromPosition: strptr("200"), ToPosition: "300", ChangeCount: 1, Formats: fmtRef()},
		},
	}
	snaps := map[int64][]laredo.Row{1: {{"id": 1, "name": nameAlice}, {"id": 2, "name": nameBob}}}
	diffs := map[string][]snapshotter.Change{
		"200": {
			{Action: laredo.ActionUpdate, Key: "2", Old: laredo.Row{"id": 2, "name": nameBob}, New: laredo.Row{"id": 2, "name": nameBob2}},
			{Action: laredo.ActionInsert, Key: "3", New: laredo.Row{"id": 3, "name": nameCarol}},
		},
		"300": {
			{Action: laredo.ActionDelete, Key: "1", Old: laredo.Row{"id": 1, "name": nameAlice}},
		},
	}
	return newArchive(t, m, snaps, diffs)
}

func TestReader_PlanAsOf(t *testing.T) {
	r := asofArchive(t)
	m, err := r.LoadManifest(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tests := []struct {
		name      string
		position  string
		wantNil   bool
		wantHead  string
		wantDiffs int
	}{
		{"predates archive", "50", true, "", 0},
		{"at snapshot boundary", "100", false, "100", 0},
		{"between boundaries excludes straddling diff", "150", false, "100", 0},
		{"at first diff boundary", "200", false, "200", 1},
		{"at head", "300", false, "300", 2},
		{"beyond head clamps to head", "999", false, "300", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := r.PlanAsOf(m, tt.position, posCmp)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if tt.wantNil {
				if plan != nil {
					t.Fatalf("plan = %+v, want nil", plan)
				}
				return
			}
			if plan == nil || plan.Snapshot == nil || plan.Snapshot.ToPosition != "100" {
				t.Fatalf("plan = %+v, want snapshot@100", plan)
			}
			if plan.HeadPosition != tt.wantHead || len(plan.Diffs) != tt.wantDiffs {
				t.Fatalf("head=%s diffs=%d, want head=%s diffs=%d", plan.HeadPosition, len(plan.Diffs), tt.wantHead, tt.wantDiffs)
			}
		})
	}
}

func TestReader_ReconstructAsOf(t *testing.T) {
	r := asofArchive(t)
	ctx := context.Background()

	names := func(rec *snapshotter.Reconstruction) map[string]string {
		out := make(map[string]string, len(rec.Rows))
		for _, row := range rec.Rows {
			out[fmt.Sprintf("%v", row["id"])] = row.GetString("name")
		}
		return out
	}

	t.Run("at snapshot", func(t *testing.T) {
		rec, err := r.ReconstructAsOf(ctx, "100", []string{"id"}, posCmp)
		if err != nil || rec == nil {
			t.Fatalf("rec=%v err=%v", rec, err)
		}
		got := names(rec)
		if rec.Position != "100" || len(got) != 2 || got["1"] != nameAlice || got["2"] != nameBob {
			t.Fatalf("as-of 100 = %v @ %s, want {1:alice 2:bob} @ 100", got, rec.Position)
		}
	})

	t.Run("after first diff", func(t *testing.T) {
		rec, err := r.ReconstructAsOf(ctx, "200", []string{"id"}, posCmp)
		if err != nil || rec == nil {
			t.Fatalf("rec=%v err=%v", rec, err)
		}
		got := names(rec)
		if rec.Position != "200" || len(got) != 3 || got["2"] != nameBob2 || got["3"] != nameCarol || got["1"] != nameAlice {
			t.Fatalf("as-of 200 = %v @ %s, want {1:alice 2:bob2 3:carol} @ 200", got, rec.Position)
		}
	})

	t.Run("at head (alice deleted)", func(t *testing.T) {
		rec, err := r.ReconstructAsOf(ctx, "300", []string{"id"}, posCmp)
		if err != nil || rec == nil {
			t.Fatalf("rec=%v err=%v", rec, err)
		}
		got := names(rec)
		if rec.Position != "300" || len(got) != 2 || got["2"] != nameBob2 || got["3"] != nameCarol {
			t.Fatalf("as-of 300 = %v @ %s, want {2:bob2 3:carol} @ 300", got, rec.Position)
		}
		if _, ok := got["1"]; ok {
			t.Fatal("alice should have been deleted by the 200→300 diff")
		}
	})

	t.Run("between boundaries reflects the earlier boundary", func(t *testing.T) {
		rec, err := r.ReconstructAsOf(ctx, "150", []string{"id"}, posCmp)
		if err != nil || rec == nil {
			t.Fatalf("rec=%v err=%v", rec, err)
		}
		if rec.Position != "100" || len(rec.Rows) != 2 {
			t.Fatalf("as-of 150 = %d rows @ %s, want 2 @ 100 (straddling diff excluded)", len(rec.Rows), rec.Position)
		}
	})

	t.Run("predates archive yields nil", func(t *testing.T) {
		rec, err := r.ReconstructAsOf(ctx, "50", []string{"id"}, posCmp)
		if err != nil || rec != nil {
			t.Fatalf("rec=%v err=%v, want nil, nil", rec, err)
		}
	})
}
