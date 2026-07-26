package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
	"github.com/zourzouvillys/laredo/source/archive"
	"github.com/zourzouvillys/laredo/source/testsource"
)

// writeArchive writes a base snapshot at position "1" (alice) and a diff 1→2
// (insert bob); head = "2". Mirrors the snapshotter's on-disk layout.
func writeArchive(t *testing.T, dir, prefix string) {
	t.Helper()
	dest := local.New(dir)
	f := jsonl.New()
	ctx := context.Background()
	put := func(art snapshotter.Artifact, payload []byte) {
		if _, _, err := dest.Put(ctx, snapshotter.ArtifactObjectKey(prefix, art, f.Extension()), bytes.NewReader(payload)); err != nil {
			t.Fatalf("put artifact: %v", err)
		}
	}

	snapArt := snapshotter.Artifact{Kind: snapshotter.KindSnapshot, Epoch: 1, ToPosition: "0/1", RowCount: 1, Formats: map[string]snapshotter.FormatRef{"jsonl": {}}}
	var sb bytes.Buffer
	if err := f.WriteSnapshot(&sb, []laredo.Row{{"id": 1, "name": "alice"}}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	put(snapArt, sb.Bytes())

	from := "0/1"
	diffArt := snapshotter.Artifact{Kind: snapshotter.KindDiff, Epoch: 1, FromPosition: &from, ToPosition: "0/2", ChangeCount: 1, Formats: map[string]snapshotter.FormatRef{"jsonl": {}}}
	var db bytes.Buffer
	if err := f.WriteDiff(&db, []snapshotter.Change{{Action: laredo.ActionInsert, Key: "2", New: laredo.Row{"id": 2, "name": "bob"}}}); err != nil {
		t.Fatalf("write diff: %v", err)
	}
	put(diffArt, db.Bytes())

	m := snapshotter.Manifest{
		ManifestVersion: snapshotter.ManifestVersion,
		Table:           "public.events",
		Epoch:           1,
		HeadPosition:    "0/2",
		Artifacts:       []snapshotter.Artifact{snapArt, diffArt},
	}
	data, _ := json.Marshal(m)
	if _, _, err := dest.Put(ctx, snapshotter.ManifestObjectKey(prefix), bytes.NewReader(data)); err != nil {
		t.Fatalf("put manifest: %v", err)
	}
}

func TestReconstructArchive_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	const prefix = "public.events/"
	writeArchive(t, dir, prefix)

	base := reconstructOpts{store: "local", path: dir, keyPrefix: prefix, format: "jsonl", keyFields: []string{"id"}}

	// As of the head: both rows.
	atHead := base
	atHead.at = "0/2"
	rec, err := reconstructArchive(atHead)
	if err != nil {
		t.Fatalf("reconstruct @0/2: %v", err)
	}
	if rec == nil || len(rec.Rows) != 2 {
		t.Fatalf("expected 2 rows at head, got %v", rec)
	}
	if rec.Position != "0/2" {
		t.Errorf("position: got %q, want 0/2", rec.Position)
	}

	// As of the base snapshot only: alice, before bob's diff.
	atBase := base
	atBase.at = "0/1"
	rec, err = reconstructArchive(atBase)
	if err != nil {
		t.Fatalf("reconstruct @0/1: %v", err)
	}
	if rec == nil || len(rec.Rows) != 1 {
		t.Fatalf("expected 1 row at base, got %v", rec)
	}
	if rec.Rows[0]["name"] != "alice" {
		t.Errorf("expected alice, got %v", rec.Rows[0])
	}
}

// TestExportArchive_RoundTrip exports an in-memory source into an archive, then
// replays it through the archive source — proving export produces something the
// consumer can read, with the schema preserved end-to-end.
func TestExportArchive_RoundTrip(t *testing.T) {
	tbl := laredo.Table("public", "events")
	ts := testsource.New()
	ts.SetSchema(tbl, []laredo.ColumnDefinition{
		{Name: "id", Type: "int8", PrimaryKey: true, PrimaryKeyOrdinal: 1, OrdinalPosition: 1},
		{Name: "name", Type: "text", Nullable: true, OrdinalPosition: 2},
	})
	ts.AddRow(tbl, laredo.Row{"id": 1, "name": "alice"})
	ts.AddRow(tbl, laredo.Row{"id": 2, "name": "bob"})

	dir := t.TempDir()
	const prefix = "public.events/"
	ctx := context.Background()

	n, _, err := exportArchive(ctx, ts, tbl, exportOpts{store: "local", path: dir, keyPrefix: prefix, format: "jsonl"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if n != 2 {
		t.Fatalf("exported %d rows, want 2", n)
	}

	reader, err := snapshotter.NewReader(local.New(dir), prefix, jsonl.New())
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	src := archive.New(archive.WithReader(reader), archive.Table("public", "events"), archive.KeyFields("id"))
	cols, err := src.Init(ctx, laredo.SourceConfig{Tables: []laredo.TableIdentifier{tbl}})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if got := cols[tbl]; len(got) != 2 || got[0].Type != "int8" || !got[0].PrimaryKey {
		t.Fatalf("schema not preserved through export: %+v", got)
	}
	var rows []laredo.Row
	if _, err := src.Baseline(ctx, nil, func(_ laredo.TableIdentifier, r laredo.Row) { rows = append(rows, r) }); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("replayed %d rows, want 2", len(rows))
	}
}

func TestReconstructArchive_BadStore(t *testing.T) {
	_, err := reconstructArchive(reconstructOpts{store: "gcs", at: "0/1", format: "jsonl"})
	if err == nil {
		t.Fatal("expected error for unknown store")
	}
}
