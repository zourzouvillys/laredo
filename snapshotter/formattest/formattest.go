// Package formattest provides a shared conformance suite for snapshotter.Format
// implementations so every format round-trips identically.
package formattest

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
)

// Run exercises a Format's snapshot and diff round-trips, including the empty
// case. Values are restricted to types that survive both JSON and protobuf
// Struct encoding (strings, float64, bool, nil) — the same shapes a fan-out row
// carries on the wire.
func Run(t *testing.T, f snapshotter.Format) {
	t.Helper()

	t.Run("meta", func(t *testing.T) {
		if f.FormatID() == "" || f.Extension() == "" {
			t.Fatalf("format must report id and extension, got %q %q", f.FormatID(), f.Extension())
		}
	})

	t.Run("snapshot round-trip", func(t *testing.T) {
		rows := []laredo.Row{
			{"id": "1", "name": "alice", "score": float64(42), "active": true},
			{"id": "2", "name": "bob", "score": float64(0), "active": false, "note": nil},
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
			t.Fatalf("snapshot mismatch:\n got %#v\nwant %#v", got, rows)
		}
	})

	t.Run("diff round-trip", func(t *testing.T) {
		changes := []snapshotter.Change{
			{Action: laredo.ActionInsert, Key: "3", New: laredo.Row{"id": "3", "name": "carol"}},
			{Action: laredo.ActionUpdate, Key: "1", Old: laredo.Row{"id": "1", "name": "alice"}, New: laredo.Row{"id": "1", "name": "alice2"}},
			{Action: laredo.ActionDelete, Key: "2", Old: laredo.Row{"id": "2", "name": "bob"}},
		}
		var buf bytes.Buffer
		if err := f.WriteDiff(&buf, changes); err != nil {
			t.Fatalf("WriteDiff: %v", err)
		}
		got, err := f.ReadDiff(&buf)
		if err != nil {
			t.Fatalf("ReadDiff: %v", err)
		}
		if !reflect.DeepEqual(got, changes) {
			t.Fatalf("diff mismatch:\n got %#v\nwant %#v", got, changes)
		}
	})

	t.Run("empty", func(t *testing.T) {
		var snap, diff bytes.Buffer
		if err := f.WriteSnapshot(&snap, nil); err != nil {
			t.Fatalf("WriteSnapshot empty: %v", err)
		}
		if err := f.WriteDiff(&diff, nil); err != nil {
			t.Fatalf("WriteDiff empty: %v", err)
		}
		rows, err := f.ReadSnapshot(&snap)
		if err != nil || len(rows) != 0 {
			t.Fatalf("ReadSnapshot empty: rows=%v err=%v", rows, err)
		}
		changes, err := f.ReadDiff(&diff)
		if err != nil || len(changes) != 0 {
			t.Fatalf("ReadDiff empty: changes=%v err=%v", changes, err)
		}
	})
}
