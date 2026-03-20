package laredo

import "testing"

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
