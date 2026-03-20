package testutil

import (
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

func TestSampleTable(t *testing.T) {
	table := SampleTable()
	if table.Schema != "public" || table.Table != "test_table" {
		t.Errorf("SampleTable() = %v, want public.test_table", table)
	}
}

func TestSampleColumns(t *testing.T) {
	cols := SampleColumns()
	if len(cols) != 3 {
		t.Fatalf("SampleColumns() returned %d columns, want 3", len(cols))
	}
	if cols[0].Name != "id" || !cols[0].PrimaryKey {
		t.Errorf("first column should be id (PK), got %+v", cols[0])
	}
}

func TestSampleRow(t *testing.T) {
	row := SampleRow(1, "alice")
	if row["id"] != 1 || row["name"] != "alice" {
		t.Errorf("SampleRow(1, alice) = %v", row)
	}
}

func TestSampleChangeEvent(t *testing.T) {
	evt := SampleChangeEvent(laredo.ActionInsert, 42, "bob")
	if evt.Action != laredo.ActionInsert {
		t.Errorf("expected INSERT, got %v", evt.Action)
	}
	if evt.NewValues["id"] != 42 {
		t.Errorf("expected id=42, got %v", evt.NewValues["id"])
	}
	if evt.Table.String() != "public.test_table" {
		t.Errorf("expected public.test_table, got %v", evt.Table)
	}
}

func TestAssertEventually_ImmediatePass(t *testing.T) {
	AssertEventually(t, 100*time.Millisecond, func() bool { return true })
}

func TestAssertEventually_DelayedPass(t *testing.T) {
	start := time.Now()
	ready := make(chan struct{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(ready)
	}()

	AssertEventually(t, 1*time.Second, func() bool {
		select {
		case <-ready:
			return true
		default:
			return false
		}
	})

	elapsed := time.Since(start)
	if elapsed < 20*time.Millisecond {
		t.Errorf("resolved too fast: %v", elapsed)
	}
}
