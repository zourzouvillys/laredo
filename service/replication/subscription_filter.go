package replication

import (
	"fmt"
	"strings"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/target/fanout"
)

// subscriptionFilter is a compiled, AND-combined set of column predicates that a
// client supplies on Sync (SyncRequest.filters). It is applied uniformly to
// snapshot rows and change entries so a subscriber sees one consistent
// partition / slice of the table across the snapshot, catch-up, and live
// phases. A nil *subscriptionFilter matches everything (the no-filter case), so
// callers can use it unconditionally.
type subscriptionFilter struct {
	preds []compiledPredicate
}

type predKind int

const (
	predEquals predKind = iota
	predPrefix
	predIn
)

type compiledPredicate struct {
	field  string
	kind   predKind
	equals any    // normalized comparison value (predEquals)
	prefix string // predPrefix
	in     []any  // normalized comparison values (predIn)
}

// compileSubscriptionFilter validates and compiles wire predicates into a
// matcher. It returns (nil, nil) when there are no predicates — the
// match-everything case. An empty field or a predicate with no match condition
// is a client error.
func compileSubscriptionFilter(preds []*v1.FieldPredicate) (*subscriptionFilter, error) {
	if len(preds) == 0 {
		return nil, nil
	}
	compiled := make([]compiledPredicate, 0, len(preds))
	for i, p := range preds {
		if p.GetField() == "" {
			return nil, fmt.Errorf("filter[%d]: field is required", i)
		}
		cp := compiledPredicate{field: p.GetField()}
		switch m := p.GetMatch().(type) {
		case *v1.FieldPredicate_Equals:
			cp.kind = predEquals
			cp.equals = normalize(m.Equals.AsInterface())
		case *v1.FieldPredicate_Prefix:
			cp.kind = predPrefix
			cp.prefix = m.Prefix
		case *v1.FieldPredicate_In:
			cp.kind = predIn
			for _, v := range m.In.GetValues() {
				cp.in = append(cp.in, normalize(v.AsInterface()))
			}
		default:
			return nil, fmt.Errorf("filter[%d] (field %q): a match condition (equals, prefix, or in) is required", i, p.GetField())
		}
		compiled = append(compiled, cp)
	}
	return &subscriptionFilter{preds: compiled}, nil
}

// matchRow reports whether a row satisfies every predicate. A nil filter
// matches all rows.
func (f *subscriptionFilter) matchRow(row laredo.Row) bool {
	if f == nil {
		return true
	}
	for _, p := range f.preds {
		if !p.match(row) {
			return false
		}
	}
	return true
}

// matchEntry reports whether a journal entry should reach a filtered subscriber.
// INSERT and UPDATE are evaluated against the post-change row; DELETE against
// the pre-change row; TRUNCATE (also the schema-change marker) always passes, as
// it is structural and applies to the whole replica. A nil filter matches
// everything.
func (f *subscriptionFilter) matchEntry(e fanout.JournalEntry) bool {
	if f == nil {
		return true
	}
	switch e.Action {
	case laredo.ActionDelete:
		return f.matchRow(e.OldValues)
	case laredo.ActionTruncate:
		return true
	default: // ActionInsert, ActionUpdate
		return f.matchRow(e.NewValues)
	}
}

func (p compiledPredicate) match(row laredo.Row) bool {
	v, ok := row[p.field]
	if !ok || v == nil {
		return false
	}
	switch p.kind {
	case predEquals:
		return normalize(v) == p.equals
	case predPrefix:
		s, isStr := v.(string)
		return isStr && strings.HasPrefix(s, p.prefix)
	case predIn:
		nv := normalize(v)
		for _, want := range p.in {
			if nv == want {
				return true
			}
		}
		return false
	}
	return false
}

// normalize coerces a value into a comparable canonical form: every numeric type
// becomes float64, and other scalars (string, bool) are left as-is. structpb
// carries all JSON numbers as float64, so wire-side predicate values arrive that
// way; normalizing the row value too lets a predicate of 42 match a column
// stored as int64(42) or float64(42).
func normalize(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int8:
		return float64(n)
	case int16:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint8:
		return float64(n)
	case uint16:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	case float32:
		return float64(n)
	default:
		return v
	}
}
