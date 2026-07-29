package laredo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
	"github.com/zourzouvillys/laredo/source/archive"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// writeArchiveSnapshot writes a single-epoch base snapshot plus a manifest at
// head pos — the minimal archive the archive source can baseline from. Writing it
// again at a later epoch/position with no diff chain from the previous head is a
// wholesale replacement.
func writeArchiveSnapshot(t *testing.T, dir, prefix string, epoch int64, pos string, rows []laredo.Row) {
	t.Helper()
	ctx := context.Background()
	dest := local.New(dir)
	f := jsonl.New()
	art := snapshotter.Artifact{
		Kind: snapshotter.KindSnapshot, Epoch: epoch, ToPosition: pos,
		RowCount: int64(len(rows)), Formats: map[string]snapshotter.FormatRef{"jsonl": {}},
	}
	var b bytes.Buffer
	if err := f.WriteSnapshot(&b, rows); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if _, _, err := dest.Put(ctx, snapshotter.ArtifactObjectKey(prefix, art, f.Extension()), bytes.NewReader(b.Bytes())); err != nil {
		t.Fatalf("put artifact: %v", err)
	}
	m := snapshotter.Manifest{
		ManifestVersion: snapshotter.ManifestVersion, Table: "public.events",
		Epoch: epoch, HeadPosition: pos, Artifacts: []snapshotter.Artifact{art},
	}
	data, _ := json.Marshal(m)
	if _, _, err := dest.Put(ctx, snapshotter.ManifestObjectKey(prefix), bytes.NewReader(data)); err != nil {
		t.Fatalf("put manifest: %v", err)
	}
}

// TestEngine_ReBaselineObserved drives a real archive source through the engine
// and verifies that a wholesale archive replacement fires OnReBaselineTriggered
// and repopulates the target — exercising the engine's re-baseline hook end to
// end (no mocks).
func TestEngine_ReBaselineObserved(t *testing.T) {
	dir := t.TempDir()
	const prefix = "public.events/"
	tbl := laredo.Table("public", "events")
	writeArchiveSnapshot(t, dir, prefix, 1, "0/1", []laredo.Row{{"id": 1, "name": "alice"}})

	reader, err := snapshotter.NewReader(local.New(dir), prefix, jsonl.New())
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	src := archive.New(
		archive.WithReader(reader),
		archive.Table("public", "events"),
		archive.KeyFields("id"),
		archive.Follow(true),
		archive.PollInterval(10*time.Millisecond),
	)

	obs := &testutil.TestObserver{}
	target := memory.NewIndexedTarget()
	e, errs := laredo.NewEngine(
		laredo.WithSource("seed", src),
		laredo.WithPipeline("seed", tbl, target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("new engine: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = e.Stop(ctx) }()

	testutil.AssertEventually(t, 3*time.Second, func() bool {
		return len(obs.EventsByType("BaselineCompleted")) > 0
	}, "initial baseline did not complete")

	// Replace the archive wholesale: a new epoch whose base position no longer
	// continues the follower's position.
	writeArchiveSnapshot(t, dir, prefix, 2, "5/0", []laredo.Row{{"id": 9, "name": "zoe"}})

	testutil.AssertEventually(t, 3*time.Second, func() bool {
		evs := obs.EventsByType("ReBaselineTriggered")
		return len(evs) > 0 && evs[0].Data["sourceID"] == "seed"
	}, "re-baseline was not observed after archive replacement")
}
