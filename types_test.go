package laredo

import (
	"slices"
	"testing"
)

func TestTableIdentifier_String(t *testing.T) {
	tests := []struct {
		schema, table string
		want          string
	}{
		{"public", "users", "public.users"},
		{"myschema", "config_document", "myschema.config_document"},
	}
	for _, tt := range tests {
		got := Table(tt.schema, tt.table).String()
		if got != tt.want {
			t.Errorf("Table(%q, %q).String() = %q, want %q", tt.schema, tt.table, got, tt.want)
		}
	}
}

func TestTableIdentifier_MarshalText(t *testing.T) {
	ti := Table("public", "users")
	got, err := ti.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error: %v", err)
	}
	if string(got) != ti.String() {
		t.Errorf("MarshalText() = %q, want %q", got, ti.String())
	}
}

func TestTableIdentifier_UnmarshalText(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    TableIdentifier
		wantErr bool
	}{
		{"normal", "public.users", Table("public", "users"), false},
		{"dots in table", "a.b.c", Table("a", "b.c"), false},
		{"empty", "", TableIdentifier{}, true},
		{"no dot", "nodot", TableIdentifier{}, true},
		{"empty schema", ".table", TableIdentifier{}, true},
		{"empty table", "schema.", TableIdentifier{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got TableIdentifier
			err := got.UnmarshalText([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnmarshalText(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("UnmarshalText(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTableIdentifier_RoundTrip(t *testing.T) {
	original := Table("myschema", "my_table")
	text, err := original.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error: %v", err)
	}
	var restored TableIdentifier
	if err := restored.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText() error: %v", err)
	}
	if restored != original {
		t.Errorf("round-trip: got %v, want %v", restored, original)
	}
}

func TestRow_GetString(t *testing.T) {
	r := Row{"name": "alice", "age": 30, "nil_val": nil}

	if got := r.GetString("name"); got != "alice" {
		t.Errorf("GetString(name) = %q, want %q", got, "alice")
	}
	if got := r.GetString("age"); got != "" {
		t.Errorf("GetString(age) = %q, want empty (not a string)", got)
	}
	if got := r.GetString("missing"); got != "" {
		t.Errorf("GetString(missing) = %q, want empty", got)
	}
	if got := r.GetString("nil_val"); got != "" {
		t.Errorf("GetString(nil_val) = %q, want empty", got)
	}
}

func TestRow_Get(t *testing.T) {
	tests := []struct {
		name    string
		row     Row
		key     string
		wantVal Value
		wantOK  bool
	}{
		{"exists non-nil", Row{"k": 42}, "k", 42, true},
		{"exists nil", Row{"k": nil}, "k", nil, true},
		{"missing", Row{"k": 1}, "other", nil, false},
		{"empty row", Row{}, "k", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := tt.row.Get(tt.key)
			if ok != tt.wantOK {
				t.Errorf("Get(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
			}
			if v != tt.wantVal {
				t.Errorf("Get(%q) value = %v, want %v", tt.key, v, tt.wantVal)
			}
		})
	}
}

func TestRow_Keys(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		want []string
	}{
		{"multiple keys sorted", Row{"c": 3, "a": 1, "b": 2}, []string{"a", "b", "c"}},
		{"empty row", Row{}, nil},
		{"single key", Row{"x": 1}, []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slices.Collect(tt.row.Keys())
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Keys() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRow_Without(t *testing.T) {
	r := Row{"a": 1, "b": 2, "c": 3, "d": 4}

	got := r.Without("b", "d")

	if len(got) != 2 {
		t.Fatalf("Without(b, d) returned %d keys, want 2", len(got))
	}
	if got["a"] != 1 || got["c"] != 3 {
		t.Errorf("Without(b, d) = %v, want {a:1, c:3}", got)
	}

	// Original unchanged
	if len(r) != 4 {
		t.Errorf("original row modified: len=%d, want 4", len(r))
	}
}

func TestRow_Without_Empty(t *testing.T) {
	r := Row{"a": 1}
	got := r.Without()
	if len(got) != 1 || got["a"] != 1 {
		t.Errorf("Without() = %v, want {a:1}", got)
	}
}

func TestChangeAction_String(t *testing.T) {
	tests := []struct {
		action ChangeAction
		want   string
	}{
		{ActionInsert, "INSERT"},
		{ActionUpdate, "UPDATE"},
		{ActionDelete, "DELETE"},
		{ActionTruncate, "TRUNCATE"},
		{ChangeAction(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.action.String(); got != tt.want {
			t.Errorf("ChangeAction(%d).String() = %q, want %q", tt.action, got, tt.want)
		}
	}
}
