package deadletter

import (
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestMemoryStore_WriteAndRead(t *testing.T) {
	s := NewMemoryStore()

	event := testutil.SampleChangeEvent(laredo.ActionInsert, 1, "alice")
	errInfo := laredo.ErrorInfo{Message: "test error"}

	if err := s.Write("p1", event, errInfo); err != nil {
		t.Fatalf("write: %v", err)
	}

	entries, err := s.Read("p1", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].PipelineID != "p1" {
		t.Errorf("pipeline ID: %s", entries[0].PipelineID)
	}
	if entries[0].Error.Message != "test error" {
		t.Errorf("error message: %s", entries[0].Error.Message)
	}
}

func TestMemoryStore_ReadLimit(t *testing.T) {
	s := NewMemoryStore()

	for i := range 5 {
		event := testutil.SampleChangeEvent(laredo.ActionInsert, i, "row")
		_ = s.Write("p1", event, laredo.ErrorInfo{})
	}

	entries, _ := s.Read("p1", 3)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries with limit, got %d", len(entries))
	}

	all, _ := s.Read("p1", 0)
	if len(all) != 5 {
		t.Errorf("expected 5 entries without limit, got %d", len(all))
	}
}

func TestMemoryStore_ReadEmpty(t *testing.T) {
	s := NewMemoryStore()
	entries, err := s.Read("nonexistent", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestMemoryStore_Purge(t *testing.T) {
	s := NewMemoryStore()

	event := testutil.SampleChangeEvent(laredo.ActionInsert, 1, "alice")
	_ = s.Write("p1", event, laredo.ErrorInfo{})
	_ = s.Write("p2", event, laredo.ErrorInfo{})

	if err := s.Purge("p1"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	if s.Count("p1") != 0 {
		t.Error("expected p1 purged")
	}
	if s.Count("p2") != 1 {
		t.Error("expected p2 unchanged")
	}
}

func TestMemoryStore_CountAndTotal(t *testing.T) {
	s := NewMemoryStore()

	event := testutil.SampleChangeEvent(laredo.ActionInsert, 1, "alice")
	_ = s.Write("p1", event, laredo.ErrorInfo{})
	_ = s.Write("p1", event, laredo.ErrorInfo{})
	_ = s.Write("p2", event, laredo.ErrorInfo{})

	if s.Count("p1") != 2 {
		t.Errorf("p1 count: %d", s.Count("p1"))
	}
	if s.Total() != 3 {
		t.Errorf("total: %d", s.Total())
	}
}

func TestMemoryStore_MultiplePipelines(t *testing.T) {
	s := NewMemoryStore()

	_ = s.Write("p1", testutil.SampleChangeEvent(laredo.ActionInsert, 1, "a"), laredo.ErrorInfo{})
	_ = s.Write("p2", testutil.SampleChangeEvent(laredo.ActionInsert, 2, "b"), laredo.ErrorInfo{})

	p1, _ := s.Read("p1", 0)
	p2, _ := s.Read("p2", 0)

	if len(p1) != 1 || len(p2) != 1 {
		t.Errorf("expected 1 each, got p1=%d p2=%d", len(p1), len(p2))
	}
}
