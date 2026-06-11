package jsonl

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/formattest"
)

func TestJSONL_Conformance(t *testing.T) {
	formattest.Run(t, New())
}

func TestJSONL_SnapshotRoundTrip(t *testing.T) {
	f := New()
	rows := []laredo.Row{
		{"id": "1", "name": "alice"},
		{"id": "2", "name": "bob", "active": true},
	}
	var buf bytes.Buffer
	if err := f.WriteSnapshot(&buf, rows); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	got, err := f.ReadSnapshot(&buf)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Fatalf("round trip mismatch:\n got %v\nwant %v", got, rows)
	}
}

func TestJSONL_DiffRoundTrip(t *testing.T) {
	f := New()
	changes := []snapshotter.Change{
		{Action: laredo.ActionInsert, Key: "3", New: laredo.Row{"id": "3", "name": "carol"}},
		{Action: laredo.ActionUpdate, Key: "1", Old: laredo.Row{"id": "1", "name": "alice"}, New: laredo.Row{"id": "1", "name": "alice2"}},
		{Action: laredo.ActionDelete, Key: "2", Old: laredo.Row{"id": "2", "name": "bob"}},
	}
	var buf bytes.Buffer
	if err := f.WriteDiff(&buf, changes); err != nil {
		t.Fatalf("WriteDiff: %v", err)
	}
	// Actions should serialize as readable strings.
	if !bytes.Contains(buf.Bytes(), []byte(`"action":"INSERT"`)) {
		t.Fatalf("expected readable action strings, got: %s", buf.String())
	}
	got, err := f.ReadDiff(&buf)
	if err != nil {
		t.Fatalf("ReadDiff: %v", err)
	}
	if !reflect.DeepEqual(got, changes) {
		t.Fatalf("round trip mismatch:\n got %v\nwant %v", got, changes)
	}
}

func TestJSONL_FormatMeta(t *testing.T) {
	f := New()
	if f.FormatID() != "jsonl" || f.Extension() != ".jsonl" {
		t.Fatalf("unexpected format meta: %q %q", f.FormatID(), f.Extension())
	}
}
