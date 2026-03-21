package transform

import (
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

const testName = "alice"

var table = laredo.Table("public", "test")

func TestDropFields(t *testing.T) {
	tr := &DropFields{Fields: []string{"secret", "internal"}}

	row := laredo.Row{"name": testName, "secret": "s3cr3t", "internal": "data", "age": 30}
	got := tr.Transform(table, row)

	if len(got) != 2 {
		t.Fatalf("expected 2 fields, got %d: %v", len(got), got)
	}
	if got["name"] != testName || got["age"] != 30 {
		t.Errorf("unexpected result: %v", got)
	}
}

func TestDropFields_NonePresent(t *testing.T) {
	tr := &DropFields{Fields: []string{"missing"}}
	row := laredo.Row{"name": testName}
	got := tr.Transform(table, row)

	if got["name"] != testName || len(got) != 1 {
		t.Errorf("expected unchanged row, got %v", got)
	}
}

func TestRenameFields(t *testing.T) {
	tr := &RenameFields{Mapping: map[string]string{"old_name": "new_name", "x": "y"}}

	row := laredo.Row{"old_name": testName, "x": 1, "keep": true}
	got := tr.Transform(table, row)

	if got["new_name"] != testName {
		t.Errorf("expected new_name=alice, got %v", got["new_name"])
	}
	if got["y"] != 1 {
		t.Errorf("expected y=1, got %v", got["y"])
	}
	if got["keep"] != true {
		t.Errorf("expected keep=true, got %v", got["keep"])
	}
	if _, ok := got["old_name"]; ok {
		t.Error("old_name should have been renamed")
	}
	if _, ok := got["x"]; ok {
		t.Error("x should have been renamed")
	}
}

func TestAddTimestamp(t *testing.T) {
	tr := &AddTimestamp{Field: "synced_at"}

	before := time.Now().UTC()
	row := laredo.Row{"name": testName}
	got := tr.Transform(table, row)
	after := time.Now().UTC()

	ts, ok := got["synced_at"].(time.Time)
	if !ok {
		t.Fatalf("synced_at is not time.Time: %T", got["synced_at"])
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("synced_at %v not between %v and %v", ts, before, after)
	}
	if got["name"] != testName {
		t.Errorf("original field missing: %v", got)
	}
}
