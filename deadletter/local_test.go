package deadletter

import (
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestLocalStore_WriteAndRead(t *testing.T) {
	s := NewLocalStore(t.TempDir())

	event := testutil.SampleChangeEvent(laredo.ActionInsert, 1, "alice")
	errInfo := laredo.ErrorInfo{Message: "test error"}

	if err := s.Write("p1", event, errInfo); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.Write("p1", testutil.SampleChangeEvent(laredo.ActionInsert, 2, "bob"), laredo.ErrorInfo{}); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	entries, err := s.Read("p1", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].PipelineID != "p1" {
		t.Errorf("pipeline ID: %s", entries[0].PipelineID)
	}
	if entries[0].Error.Message != "test error" {
		t.Errorf("error message: %s", entries[0].Error.Message)
	}
}

func TestLocalStore_ReadLimit(t *testing.T) {
	s := NewLocalStore(t.TempDir())

	for i := range 5 {
		_ = s.Write("p1", testutil.SampleChangeEvent(laredo.ActionInsert, i, "row"), laredo.ErrorInfo{})
	}

	entries, _ := s.Read("p1", 3)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries with limit, got %d", len(entries))
	}
}

func TestLocalStore_ReadEmpty(t *testing.T) {
	s := NewLocalStore(t.TempDir())
	entries, err := s.Read("nonexistent", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestLocalStore_Purge(t *testing.T) {
	s := NewLocalStore(t.TempDir())

	_ = s.Write("p1", testutil.SampleChangeEvent(laredo.ActionInsert, 1, "a"), laredo.ErrorInfo{})
	_ = s.Write("p2", testutil.SampleChangeEvent(laredo.ActionInsert, 2, "b"), laredo.ErrorInfo{})

	if err := s.Purge("p1"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	p1, _ := s.Read("p1", 0)
	p2, _ := s.Read("p2", 0)
	if len(p1) != 0 {
		t.Error("expected p1 purged")
	}
	if len(p2) != 1 {
		t.Error("expected p2 unchanged")
	}
}

func TestLocalStore_MultiplePipelines(t *testing.T) {
	s := NewLocalStore(t.TempDir())

	_ = s.Write("p1", testutil.SampleChangeEvent(laredo.ActionInsert, 1, "a"), laredo.ErrorInfo{})
	_ = s.Write("p2", testutil.SampleChangeEvent(laredo.ActionInsert, 2, "b"), laredo.ErrorInfo{})

	p1, _ := s.Read("p1", 0)
	p2, _ := s.Read("p2", 0)
	if len(p1) != 1 || len(p2) != 1 {
		t.Errorf("expected 1 each, got p1=%d p2=%d", len(p1), len(p2))
	}
}

func TestLocalStore_Persistence(t *testing.T) {
	dir := t.TempDir()

	// Write with one instance.
	s1 := NewLocalStore(dir)
	_ = s1.Write("p1", testutil.SampleChangeEvent(laredo.ActionInsert, 1, "persisted"), laredo.ErrorInfo{})

	// Read with a new instance.
	s2 := NewLocalStore(dir)
	entries, err := s2.Read("p1", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry from disk, got %d", len(entries))
	}
}

func TestLocalStore_ChangeEventPreserved(t *testing.T) {
	s := NewLocalStore(t.TempDir())

	event := laredo.ChangeEvent{
		Table:     testutil.SampleTable(),
		Action:    laredo.ActionUpdate,
		Position:  uint64(42),
		Timestamp: time.Now().Truncate(time.Second),
		NewValues: laredo.Row{"id": 1, "name": "new"},
		OldValues: laredo.Row{"id": 1, "name": "old"},
	}

	_ = s.Write("p1", event, laredo.ErrorInfo{Message: "fail"})

	entries, _ := s.Read("p1", 0)
	if len(entries) != 1 {
		t.Fatalf("expected 1, got %d", len(entries))
	}

	got := entries[0]
	if got.Change.Action != laredo.ActionUpdate {
		t.Errorf("action: %v", got.Change.Action)
	}
	if got.Change.Table != testutil.SampleTable() {
		t.Errorf("table: %v", got.Change.Table)
	}
}
