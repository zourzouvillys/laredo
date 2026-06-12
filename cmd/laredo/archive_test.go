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
)

func TestLSNCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int // sign
	}{
		{"0/1", "0/2", -1},
		{"0/2", "0/1", 1},
		{"1/0", "0/FFFFFFFF", 1},
		{"0/A", "0/a", 0}, // case-insensitive hex
		{"", "0/1", -1},   // empty sorts lowest
		{"0/1", "", 1},
		{"", "", 0},
		{"garbage", "0/1", -1}, // unparseable sorts lowest
		{"0/10", "0/2", 1},     // hex, not lexical
	}
	for _, c := range cases {
		got := lsnCompare(c.a, c.b)
		if sign(got) != c.want {
			t.Errorf("lsnCompare(%q,%q) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

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

func TestReconstructArchive_BadStore(t *testing.T) {
	_, err := reconstructArchive(reconstructOpts{store: "gcs", at: "0/1", format: "jsonl"})
	if err == nil {
		t.Fatal("expected error for unknown store")
	}
}
