package testutil

import (
	"testing"

	"github.com/zourzouvillys/laredo"
)

func TestRandomSchema(t *testing.T) {
	schema := RandomSchema(5)
	if len(schema) != 5 {
		t.Fatalf("expected 5 columns, got %d", len(schema))
	}
	if schema[0].Name != "id" || !schema[0].PrimaryKey {
		t.Error("first column should be id primary key")
	}
	for i := 1; i < len(schema); i++ {
		if schema[i].Name == "" {
			t.Errorf("column %d has empty name", i)
		}
		if schema[i].Type == "" {
			t.Errorf("column %d has empty type", i)
		}
	}
}

func TestRandomSchema_MinColumns(t *testing.T) {
	schema := RandomSchema(0)
	if len(schema) != 1 {
		t.Fatalf("expected at least 1 column, got %d", len(schema))
	}
	if schema[0].Name != "id" {
		t.Error("single column should be id")
	}
}

func TestRandomRow(t *testing.T) {
	schema := RandomSchema(4)
	row := RandomRow(schema, 42)

	if row["id"] != 42 {
		t.Errorf("expected id=42, got %v", row["id"])
	}
	for _, col := range schema {
		if _, ok := row[col.Name]; !ok {
			t.Errorf("missing column %s in row", col.Name)
		}
	}
}

func TestRandomRows(t *testing.T) {
	schema := RandomSchema(3)
	rows := RandomRows(schema, 10)

	if len(rows) != 10 {
		t.Fatalf("expected 10 rows, got %d", len(rows))
	}
	for i, row := range rows {
		if row["id"] != i+1 {
			t.Errorf("row %d: expected id=%d, got %v", i, i+1, row["id"])
		}
	}
}

func TestRandomChangeEvent(t *testing.T) {
	table := SampleTable()
	schema := RandomSchema(3)

	event := RandomChangeEvent(table, schema, 1)
	if event.Table != table {
		t.Error("wrong table")
	}
	if event.Position != 1 {
		t.Errorf("expected position=1, got %v", event.Position)
	}

	switch event.Action {
	case laredo.ActionInsert:
		if event.NewValues == nil {
			t.Error("INSERT should have NewValues")
		}
	case laredo.ActionUpdate:
		if event.OldValues == nil || event.NewValues == nil {
			t.Error("UPDATE should have OldValues and NewValues")
		}
	case laredo.ActionDelete:
		if event.OldValues == nil {
			t.Error("DELETE should have OldValues")
		}
	default:
		t.Errorf("unexpected action: %v", event.Action)
	}
}

func TestRandomChangeEvents(t *testing.T) {
	table := SampleTable()
	schema := RandomSchema(3)
	events := RandomChangeEvents(table, schema, 20)

	if len(events) != 20 {
		t.Fatalf("expected 20 events, got %d", len(events))
	}
	for i, e := range events {
		if e.Position != i+1 {
			t.Errorf("event %d: expected position=%d, got %v", i, i+1, e.Position)
		}
	}
}
