package jsonl

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

func sampleInfo() laredo.TableSnapshotInfo {
	return laredo.TableSnapshotInfo{
		Table:    laredo.Table("public", "users"),
		RowCount: 2,
		Columns: []laredo.ColumnDefinition{
			{Name: "id", Type: "integer", PrimaryKey: true},
			{Name: "name", Type: "text"},
		},
		TargetType: "memory.IndexedTarget",
	}
}

func TestSerializer_WriteRead_RoundTrip(t *testing.T) {
	s := New()

	info := sampleInfo()
	rows := []laredo.Row{
		{"id": 1, "name": "alice"},
		{"id": 2, "name": "bob"},
	}

	var buf bytes.Buffer
	if err := s.Write(info, rows, &buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	gotInfo, gotRows, err := s.Read(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if gotInfo.Table != info.Table {
		t.Errorf("table: expected %v, got %v", info.Table, gotInfo.Table)
	}
	if gotInfo.RowCount != info.RowCount {
		t.Errorf("row count: expected %d, got %d", info.RowCount, gotInfo.RowCount)
	}
	if gotInfo.TargetType != info.TargetType {
		t.Errorf("target type: expected %s, got %s", info.TargetType, gotInfo.TargetType)
	}
	if len(gotInfo.Columns) != len(info.Columns) {
		t.Errorf("columns: expected %d, got %d", len(info.Columns), len(gotInfo.Columns))
	}

	if len(gotRows) != len(rows) {
		t.Fatalf("rows: expected %d, got %d", len(rows), len(gotRows))
	}

	// JSON numbers decode as float64.
	if gotRows[0]["name"] != "alice" {
		t.Errorf("row[0].name: expected alice, got %v", gotRows[0]["name"])
	}
	if gotRows[1]["name"] != "bob" {
		t.Errorf("row[1].name: expected bob, got %v", gotRows[1]["name"])
	}
}

func TestSerializer_FormatID(t *testing.T) {
	s := New()
	if s.FormatID() != "jsonl" {
		t.Errorf("expected jsonl, got %s", s.FormatID())
	}
}

func TestSerializer_EmptyRows(t *testing.T) {
	s := New()

	info := sampleInfo()
	info.RowCount = 0

	var buf bytes.Buffer
	if err := s.Write(info, nil, &buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	gotInfo, gotRows, err := s.Read(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if gotInfo.Table != info.Table {
		t.Errorf("table mismatch")
	}
	if len(gotRows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(gotRows))
	}
}

func TestSerializer_ValueTypes(t *testing.T) {
	s := New()

	info := laredo.TableSnapshotInfo{
		Table:    laredo.Table("public", "test"),
		RowCount: 1,
	}

	rows := []laredo.Row{
		{
			"str":     "hello",
			"num_int": 42,
			"num_f":   3.14,
			"bool_t":  true,
			"bool_f":  false,
			"null_v":  nil,
			"nested":  map[string]any{"key": "value"},
			"arr":     []any{1, 2, 3},
		},
	}

	var buf bytes.Buffer
	if err := s.Write(info, rows, &buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, gotRows, err := s.Read(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if len(gotRows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(gotRows))
	}

	r := gotRows[0]
	if r["str"] != "hello" {
		t.Errorf("str: %v", r["str"])
	}
	// JSON numbers are float64.
	if r["num_int"] != float64(42) {
		t.Errorf("num_int: %v (type %T)", r["num_int"], r["num_int"])
	}
	if r["num_f"] != 3.14 {
		t.Errorf("num_f: %v", r["num_f"])
	}
	if r["bool_t"] != true {
		t.Errorf("bool_t: %v", r["bool_t"])
	}
	if r["bool_f"] != false {
		t.Errorf("bool_f: %v", r["bool_f"])
	}
	if r["null_v"] != nil {
		t.Errorf("null_v: %v", r["null_v"])
	}
	nested, ok := r["nested"].(map[string]any)
	if !ok || nested["key"] != "value" {
		t.Errorf("nested: %v", r["nested"])
	}
}

func TestSerializer_TimeValue(t *testing.T) {
	s := New()

	ts := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	info := laredo.TableSnapshotInfo{Table: laredo.Table("public", "t"), RowCount: 1}
	rows := []laredo.Row{{"ts": ts.Format(time.RFC3339)}}

	var buf bytes.Buffer
	if err := s.Write(info, rows, &buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, gotRows, err := s.Read(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if gotRows[0]["ts"] != "2025-06-15T10:30:00Z" {
		t.Errorf("ts: %v", gotRows[0]["ts"])
	}
}

func TestSerializer_LineFormat(t *testing.T) {
	s := New()

	info := laredo.TableSnapshotInfo{Table: laredo.Table("public", "t"), RowCount: 2}
	rows := []laredo.Row{{"a": 1}, {"b": 2}}

	var buf bytes.Buffer
	if err := s.Write(info, rows, &buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (1 header + 2 rows), got %d", len(lines))
	}
}

func TestSerializer_ReadEmptyInput(t *testing.T) {
	s := New()
	_, _, err := s.Read(strings.NewReader(""))
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestSerializer_ReadInvalidJSON(t *testing.T) {
	s := New()
	_, _, err := s.Read(strings.NewReader("not json\n"))
	if err == nil {
		t.Error("expected error for invalid header JSON")
	}
}

func TestSerializer_ReadInvalidRowJSON(t *testing.T) {
	s := New()
	input := `{"Table":{"Schema":"public","Table":"t"},"RowCount":1}
not json row`
	_, _, err := s.Read(strings.NewReader(input))
	if err == nil {
		t.Error("expected error for invalid row JSON")
	}
}
