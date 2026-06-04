// Package desttest provides a shared conformance suite for snapshotter
// Destination implementations so local, S3, and future backends behave
// identically. The destination passed to Run must address an empty namespace.
package desttest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/zourzouvillys/laredo/snapshotter"
)

// Run exercises Put/Get and the compare-and-swap semantics of a Destination.
func Run(t *testing.T, d snapshotter.Destination) {
	t.Helper()
	ctx := context.Background()

	t.Run("put and get", func(t *testing.T) {
		uri, size, err := d.Put(ctx, "obj/a.txt", bytes.NewReader([]byte("hello")))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		if uri == "" || size != 5 {
			t.Fatalf("Put uri=%q size=%d", uri, size)
		}
		data, rev, found, err := d.Get(ctx, "obj/a.txt")
		if err != nil || !found {
			t.Fatalf("Get: found=%v err=%v", found, err)
		}
		if string(data) != "hello" || rev == "" {
			t.Fatalf("Get data=%q rev=%q", data, rev)
		}
	})

	t.Run("get missing", func(t *testing.T) {
		_, _, found, err := d.Get(ctx, "does/not/exist")
		if err != nil || found {
			t.Fatalf("missing: found=%v err=%v", found, err)
		}
	})

	t.Run("cas", func(t *testing.T) {
		key := "cas.json"
		rev1, err := d.PutCAS(ctx, key, []byte(`{"v":1}`), "")
		if err != nil {
			t.Fatalf("create PutCAS: %v", err)
		}
		if _, err := d.PutCAS(ctx, key, []byte(`{"v":2}`), "stale-rev"); !errors.Is(err, snapshotter.ErrCASConflict) {
			t.Fatalf("stale rev: want ErrCASConflict, got %v", err)
		}
		rev2, err := d.PutCAS(ctx, key, []byte(`{"v":2}`), rev1)
		if err != nil {
			t.Fatalf("update PutCAS: %v", err)
		}
		if rev2 == rev1 {
			t.Fatal("rev should change after a write")
		}
		data, _, _, _ := d.Get(ctx, key)
		if string(data) != `{"v":2}` {
			t.Fatalf("content = %q, want v:2", data)
		}
	})
}
