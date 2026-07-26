package snapshotter_test

import (
	"context"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
)

// TestWriteBaseSnapshot_RoundTrip writes a one-shot base snapshot with a recorded
// schema and reads it back through a Reader, proving the artifact, manifest, and
// columns all land where a consumer expects them.
func TestWriteBaseSnapshot_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	const prefix = "public.events/"
	ctx := context.Background()
	dest := local.New(dir)
	f := jsonl.New()

	cols := []laredo.ColumnDefinition{
		{Name: "id", Type: "int8", PrimaryKey: true, PrimaryKeyOrdinal: 1, OrdinalPosition: 1},
		{Name: "name", Type: "text", Nullable: true, OrdinalPosition: 2},
	}
	rows := []laredo.Row{{"id": 1, "name": "alice"}, {"id": 2, "name": "bob"}}
	when := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	err := snapshotter.WriteBaseSnapshot(ctx, []snapshotter.Destination{dest}, prefix, []snapshotter.Format{f}, "public.events", "0/5", rows, cols, when)
	if err != nil {
		t.Fatalf("WriteBaseSnapshot: %v", err)
	}

	reader, err := snapshotter.NewReader(dest, prefix, f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	m, err := reader.LoadManifest(ctx)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.HeadPosition != "0/5" {
		t.Errorf("head: got %q, want 0/5", m.HeadPosition)
	}
	if len(m.Columns) != 2 || m.Columns[0].Name != "id" || !m.Columns[0].PrimaryKey {
		t.Errorf("schema not recorded: %+v", m.Columns)
	}
	if !m.UpdatedAt.Equal(when) {
		t.Errorf("timestamp: got %v, want %v", m.UpdatedAt, when)
	}

	got, err := reader.ReconstructAsOf(ctx, "0/5", []string{"id"}, func(_, _ string) int { return 0 })
	if err != nil {
		t.Fatalf("ReconstructAsOf: %v", err)
	}
	if got == nil || len(got.Rows) != 2 {
		t.Fatalf("expected 2 reconstructed rows, got %v", got)
	}
}

func TestWriteBaseSnapshot_Errors(t *testing.T) {
	ctx := context.Background()
	f := jsonl.New()
	if err := snapshotter.WriteBaseSnapshot(ctx, nil, "p/", []snapshotter.Format{f}, "t", "0/1", nil, nil, time.Time{}); err == nil {
		t.Error("expected an error with no destinations")
	}
	if err := snapshotter.WriteBaseSnapshot(ctx, []snapshotter.Destination{local.New(t.TempDir())}, "p/", nil, "t", "0/1", nil, nil, time.Time{}); err == nil {
		t.Error("expected an error with no formats")
	}
}
