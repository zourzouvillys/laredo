package local

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zourzouvillys/laredo/snapshotter"
)

func TestLocal_PutAndGet(t *testing.T) {
	d := New(t.TempDir())
	ctx := context.Background()

	uri, size, err := d.Put(ctx, "epoch=1/snapshot-0.jsonl", bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if size != 5 {
		t.Fatalf("size = %d, want 5", size)
	}
	if filepath.Ext(uri) != ".jsonl" {
		t.Fatalf("unexpected uri: %s", uri)
	}

	data, rev, found, err := d.Get(ctx, "epoch=1/snapshot-0.jsonl")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if string(data) != "hello" || rev == "" {
		t.Fatalf("Get data=%q rev=%q", data, rev)
	}
}

func TestLocal_GetMissing(t *testing.T) {
	d := New(t.TempDir())
	_, _, found, err := d.Get(context.Background(), "nope.json")
	if err != nil || found {
		t.Fatalf("missing object: found=%v err=%v", found, err)
	}
}

func TestLocal_PutCAS(t *testing.T) {
	d := New(t.TempDir())
	ctx := context.Background()
	key := "manifest.json"

	// First write requires empty rev (object must not exist).
	rev1, err := d.PutCAS(ctx, key, []byte(`{"v":1}`), "")
	if err != nil {
		t.Fatalf("initial PutCAS: %v", err)
	}

	// Stale rev is rejected.
	if _, err := d.PutCAS(ctx, key, []byte(`{"v":2}`), "stale"); !errors.Is(err, snapshotter.ErrCASConflict) {
		t.Fatalf("expected CAS conflict, got %v", err)
	}

	// Correct rev succeeds and returns a new rev.
	rev2, err := d.PutCAS(ctx, key, []byte(`{"v":2}`), rev1)
	if err != nil {
		t.Fatalf("PutCAS with current rev: %v", err)
	}
	if rev2 == rev1 {
		t.Fatal("rev should change after a write")
	}

	data, _, _, _ := d.Get(ctx, key)
	if string(data) != `{"v":2}` {
		t.Fatalf("content = %q, want v:2", data)
	}
}

func TestLocal_PutIsAtomic(t *testing.T) {
	base := t.TempDir()
	d := New(base)
	ctx := context.Background()
	if _, _, err := d.Put(ctx, "a/b/c.txt", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("Put nested: %v", err)
	}
	// No leftover temp files in the directory.
	entries, _ := os.ReadDir(filepath.Join(base, "a", "b"))
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}
