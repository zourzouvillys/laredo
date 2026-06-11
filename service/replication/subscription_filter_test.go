package replication

import (
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/target/fanout"
)

func eqPred(t *testing.T, field string, v any) *v1.FieldPredicate {
	t.Helper()
	val, err := structpb.NewValue(v)
	if err != nil {
		t.Fatalf("structpb.NewValue(%v): %v", v, err)
	}
	return &v1.FieldPredicate{Field: field, Match: &v1.FieldPredicate_Equals{Equals: val}}
}

func prefixPred(field, p string) *v1.FieldPredicate {
	return &v1.FieldPredicate{Field: field, Match: &v1.FieldPredicate_Prefix{Prefix: p}}
}

func inPred(t *testing.T, field string, vs ...any) *v1.FieldPredicate {
	t.Helper()
	lv, err := structpb.NewList(vs)
	if err != nil {
		t.Fatalf("structpb.NewList(%v): %v", vs, err)
	}
	return &v1.FieldPredicate{Field: field, Match: &v1.FieldPredicate_In{In: lv}}
}

func TestCompileSubscriptionFilter(t *testing.T) {
	t.Run("empty is no filter", func(t *testing.T) {
		f, err := compileSubscriptionFilter(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f != nil {
			t.Fatalf("expected nil filter for no predicates, got %#v", f)
		}
		// A nil filter matches everything.
		if !f.matchRow(laredo.Row{"x": 1}) {
			t.Fatal("nil filter should match any row")
		}
	})

	t.Run("valid predicates compile", func(t *testing.T) {
		f, err := compileSubscriptionFilter([]*v1.FieldPredicate{
			eqPred(t, "tenant_id", "acme"),
			prefixPred("key", "rulesets/"),
			inPred(t, "region", "eu", "us"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := len(f.preds); got != 3 {
			t.Fatalf("compiled %d predicates, want 3", got)
		}
	})

	t.Run("empty field is rejected", func(t *testing.T) {
		_, err := compileSubscriptionFilter([]*v1.FieldPredicate{eqPred(t, "", "x")})
		if err == nil {
			t.Fatal("expected error for empty field")
		}
	})

	t.Run("missing match condition is rejected", func(t *testing.T) {
		_, err := compileSubscriptionFilter([]*v1.FieldPredicate{{Field: "tenant_id"}})
		if err == nil {
			t.Fatal("expected error for missing match condition")
		}
	})
}

func TestSubscriptionFilter_MatchRow(t *testing.T) {
	tests := []struct {
		name  string
		preds []*v1.FieldPredicate
		row   laredo.Row
		want  bool
	}{
		{"equals string match", []*v1.FieldPredicate{eqPred(t, "tenant_id", "acme")}, laredo.Row{"tenant_id": "acme"}, true},
		{"equals string no match", []*v1.FieldPredicate{eqPred(t, "tenant_id", "acme")}, laredo.Row{"tenant_id": "other"}, false},
		{"equals missing field", []*v1.FieldPredicate{eqPred(t, "tenant_id", "acme")}, laredo.Row{"other": "acme"}, false},
		{"equals nil value", []*v1.FieldPredicate{eqPred(t, "tenant_id", "acme")}, laredo.Row{"tenant_id": nil}, false},
		{"equals int column vs number predicate", []*v1.FieldPredicate{eqPred(t, "tenant", 42)}, laredo.Row{"tenant": int64(42)}, true},
		{"equals float column vs number predicate", []*v1.FieldPredicate{eqPred(t, "tenant", 42)}, laredo.Row{"tenant": float64(42)}, true},
		{"equals number no match", []*v1.FieldPredicate{eqPred(t, "tenant", 42)}, laredo.Row{"tenant": int64(7)}, false},
		{"equals bool", []*v1.FieldPredicate{eqPred(t, "active", true)}, laredo.Row{"active": true}, true},
		{"prefix match", []*v1.FieldPredicate{prefixPred("key", "rulesets/")}, laredo.Row{"key": "rulesets/default"}, true},
		{"prefix exact", []*v1.FieldPredicate{prefixPred("key", "rulesets/")}, laredo.Row{"key": "rulesets/"}, true},
		{"prefix no match", []*v1.FieldPredicate{prefixPred("key", "rulesets/")}, laredo.Row{"key": "config/x"}, false},
		{"prefix non-string", []*v1.FieldPredicate{prefixPred("key", "1")}, laredo.Row{"key": 123}, false},
		{"in match", []*v1.FieldPredicate{inPred(t, "region", "eu", "us")}, laredo.Row{"region": "us"}, true},
		{"in no match", []*v1.FieldPredicate{inPred(t, "region", "eu", "us")}, laredo.Row{"region": "ap"}, false},
		{"in numeric match", []*v1.FieldPredicate{inPred(t, "shard", 1, 2, 3)}, laredo.Row{"shard": int64(2)}, true},
		{"AND all match", []*v1.FieldPredicate{eqPred(t, "tenant_id", "acme"), eqPred(t, "region", "eu")}, laredo.Row{"tenant_id": "acme", "region": "eu"}, true},
		{"AND one fails", []*v1.FieldPredicate{eqPred(t, "tenant_id", "acme"), eqPred(t, "region", "eu")}, laredo.Row{"tenant_id": "acme", "region": "us"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := compileSubscriptionFilter(tt.preds)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if got := f.matchRow(tt.row); got != tt.want {
				t.Errorf("matchRow(%v) = %v, want %v", tt.row, got, tt.want)
			}
		})
	}
}

func TestSubscriptionFilter_MatchEntry(t *testing.T) {
	f, err := compileSubscriptionFilter([]*v1.FieldPredicate{eqPred(t, "tenant_id", "acme")})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	acme := laredo.Row{"tenant_id": "acme", "id": 1}
	other := laredo.Row{"tenant_id": "other", "id": 2}

	tests := []struct {
		name  string
		entry fanout.JournalEntry
		want  bool
	}{
		{"insert in partition (new values)", fanout.JournalEntry{Action: laredo.ActionInsert, NewValues: acme}, true},
		{"insert out of partition", fanout.JournalEntry{Action: laredo.ActionInsert, NewValues: other}, false},
		{"update in partition (new values)", fanout.JournalEntry{Action: laredo.ActionUpdate, OldValues: acme, NewValues: acme}, true},
		{"update out of partition", fanout.JournalEntry{Action: laredo.ActionUpdate, NewValues: other}, false},
		{"delete in partition (old values)", fanout.JournalEntry{Action: laredo.ActionDelete, OldValues: acme}, true},
		{"delete out of partition", fanout.JournalEntry{Action: laredo.ActionDelete, OldValues: other}, false},
		{"truncate always passes", fanout.JournalEntry{Action: laredo.ActionTruncate}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.matchEntry(tt.entry); got != tt.want {
				t.Errorf("matchEntry(%+v) = %v, want %v", tt.entry, got, tt.want)
			}
		})
	}

	t.Run("nil filter matches every entry", func(t *testing.T) {
		var nilFilter *subscriptionFilter
		if !nilFilter.matchEntry(fanout.JournalEntry{Action: laredo.ActionInsert, NewValues: other}) {
			t.Fatal("nil filter should admit any entry")
		}
	})
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want any
	}{
		{"int", int(5), float64(5)},
		{"int8", int8(5), float64(5)},
		{"int16", int16(5), float64(5)},
		{"int32", int32(5), float64(5)},
		{"int64", int64(5), float64(5)},
		{"uint", uint(5), float64(5)},
		{"uint8", uint8(5), float64(5)},
		{"uint16", uint16(5), float64(5)},
		{"uint32", uint32(5), float64(5)},
		{"uint64", uint64(5), float64(5)},
		{"float32", float32(5), float64(5)},
		{"float64", float64(5), float64(5)},
		{"string passthrough", "x", "x"},
		{"bool passthrough", true, true},
		{"nil passthrough", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalize(tt.in); got != tt.want {
				t.Errorf("normalize(%#v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
