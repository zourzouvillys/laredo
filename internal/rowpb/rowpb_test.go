package rowpb

import (
	"database/sql/driver"
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

// TestRowToStruct_pgxNativeTypes is the regression test for the silent row
// loss. Every value here is something pgx hands back from rows.Values() and
// structpb.NewStruct rejects outright; before the conversion existed each one
// produced a nil Struct whose error was discarded, and the receiving client
// skipped the row while the journal sequence advanced regardless.
func TestRowToStruct_pgxNativeTypes(t *testing.T) {
	ts := time.Date(2026, 8, 24, 10, 30, 0, 123456789, time.UTC)
	uuid := [16]byte{0xa1, 0xb2, 0xc3, 0xd4, 0xe5, 0xf6, 0x47, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00}
	prefix := netip.MustParsePrefix("203.0.113.0/24")

	row := laredo.Row{
		"id":         int64(42),
		"created_at": ts,
		"uuid_col":   uuid,
		"inet_col":   prefix,
		"name":       "hello",
		"active":     true,
		"score":      1.5,
		"payload":    map[string]any{"nested": "value"},
		"nothing":    nil,
	}

	got, err := RowToStruct(row)
	if err != nil {
		t.Fatalf("RowToStruct: %v", err)
	}
	if got == nil {
		t.Fatal("RowToStruct returned a nil Struct with no error")
	}
	if len(got.GetFields()) != len(row) {
		t.Fatalf("got %d fields, want %d", len(got.GetFields()), len(row))
	}

	want := map[string]any{
		"id":         float64(42),
		"created_at": "2026-08-24T10:30:00.123456789Z",
		"uuid_col":   "a1b2c3d4-e5f6-4788-99aa-bbccddeeff00",
		"inet_col":   "203.0.113.0/24",
		"name":       "hello",
		"active":     true,
		"score":      1.5,
		"nothing":    nil,
	}
	asMap := got.AsMap()
	for k, w := range want {
		if asMap[k] != w {
			t.Errorf("field %q = %#v, want %#v", k, asMap[k], w)
		}
	}
	nested, ok := asMap["payload"].(map[string]any)
	if !ok || nested["nested"] != "value" {
		t.Errorf("payload = %#v, want nested map", asMap["payload"])
	}
}

// numericStub stands in for pgtype.Numeric, which reaches ToValue through
// driver.Valuer and must keep its precision as a string rather than becoming
// a float.
type numericStub struct{ s string }

func (n numericStub) Value() (driver.Value, error) { return n.s, nil }

func TestToValue_valuerKeepsPrecision(t *testing.T) {
	v, err := ToValue(numericStub{s: "12345678901234567890.0000000001"})
	if err != nil {
		t.Fatalf("ToValue: %v", err)
	}
	if got := v.GetStringValue(); got != "12345678901234567890.0000000001" {
		t.Errorf("got %q, want the numeric preserved as text", got)
	}
}

func TestToValue_rejectsUnrepresentable(t *testing.T) {
	tests := []struct {
		name string
		in   any
	}{
		{"int64 beyond 2^53", int64(1) << 54},
		{"negative int64 beyond 2^53", -(int64(1) << 54)},
		{"uint64 beyond 2^53", uint64(1) << 54},
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"unsupported type", make(chan int)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ToValue(tt.in); err == nil {
				t.Fatalf("ToValue(%v) returned no error; an unrepresentable value must "+
					"fail loudly rather than encode wrongly", tt.name)
			}
		})
	}
}

func TestToValue_safeIntegerBoundary(t *testing.T) {
	// The largest integer a float64 holds exactly must still encode.
	v, err := ToValue(int64(maxSafeInteger))
	if err != nil {
		t.Fatalf("ToValue(maxSafeInteger): %v", err)
	}
	if got := v.GetNumberValue(); got != float64(maxSafeInteger) {
		t.Errorf("got %v, want %v", got, float64(maxSafeInteger))
	}
}

func TestRowToStruct_namesTheOffendingColumn(t *testing.T) {
	_, err := RowToStruct(laredo.Row{"ok": "fine", "bad": make(chan int)})
	if err == nil {
		t.Fatal("expected an error")
	}
	// An operator reading a failed stream needs to know which column did it.
	if want := `column "bad"`; !contains(err.Error(), want) {
		t.Errorf("error %q does not name the column (want %q)", err, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
