package memory_test

import (
	"context"
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func baseline(t *testing.T, tg laredo.SyncTarget, start bool, complete bool, rows ...laredo.Row) {
	t.Helper()
	ctx := context.Background()
	tbl := testutil.SampleTable()
	if start {
		if err := tg.(laredo.SweepingTarget).OnBaselineStart(ctx, tbl); err != nil {
			t.Fatalf("start: %v", err)
		}
	}
	for _, r := range rows {
		if err := tg.OnBaselineRow(ctx, tbl, r); err != nil {
			t.Fatalf("row: %v", err)
		}
	}
	if complete {
		if err := tg.OnBaselineComplete(ctx, tbl); err != nil {
			t.Fatalf("complete: %v", err)
		}
	}
}

func TestIndexedTarget_sweep(t *testing.T) {
	ctx := context.Background()
	tg := memory.NewIndexedTarget(memory.AddIndex(laredo.IndexDefinition{Name: "by_name", Fields: []string{"name"}}))
	if err := tg.OnInit(ctx, testutil.SampleTable(), testutil.SampleColumns()); err != nil {
		t.Fatal(err)
	}
	var removed []laredo.Row
	tg.Listen(func(old, new laredo.Row) {
		if old != nil && new == nil {
			removed = append(removed, old)
		}
	})

	baseline(t, tg, true, true, testutil.SampleRow(1, "a"), testutil.SampleRow(2, "b"), testutil.SampleRow(3, "b"))

	// A failed re-baseline (no Complete) removes nothing...
	baseline(t, tg, true, false, testutil.SampleRow(1, "a"))
	if tg.Count() != 3 {
		t.Fatalf("partial baseline swept rows: count=%d", tg.Count())
	}
	// ...and its marks do not leak into the next one.
	baseline(t, tg, true, true, testutil.SampleRow(1, "a"), testutil.SampleRow(3, "b"))
	if tg.Count() != 2 {
		t.Fatalf("count=%d, want 2", tg.Count())
	}
	if _, ok := tg.Get(2); ok {
		t.Error("row 2 survived a baseline that did not contain it")
	}
	if len(removed) != 1 || removed[0]["id"] != 2 {
		t.Errorf("listeners saw removals %v, want row 2 only", removed)
	}
	// Secondary index holds row 3 once, not a copy per baseline.
	if got := tg.LookupAll("by_name", "b"); len(got) != 1 {
		t.Errorf("by_name[b] has %d rows, want 1", len(got))
	}

	// A baseline without OnBaselineStart (e.g. after a truncate) sweeps nothing.
	baseline(t, tg, false, true, testutil.SampleRow(9, "z"))
	if tg.Count() != 3 {
		t.Errorf("an unstarted baseline swept: count=%d", tg.Count())
	}
}

func TestCompiledTarget_sweep(t *testing.T) {
	ctx := context.Background()
	tg := memory.NewCompiledTarget(
		memory.Compiler(func(r laredo.Row) (any, error) { return r["name"], nil }),
		memory.KeyFields("id"),
		memory.CompiledFilter(func(r laredo.Row) bool { return r["name"] != "hidden" }),
	)
	if err := tg.OnInit(ctx, testutil.SampleTable(), testutil.SampleColumns()); err != nil {
		t.Fatal(err)
	}
	var removed []any
	tg.Listen(func(old, new any) {
		if old != nil && new == nil {
			removed = append(removed, old)
		}
	})

	baseline(t, tg, true, true, testutil.SampleRow(1, "a"), testutil.SampleRow(2, "b"))
	// Row 2 now fails the filter: like OnUpdate, the re-baseline drops it.
	baseline(t, tg, true, true, testutil.SampleRow(1, "a"), testutil.SampleRow(2, "hidden"))
	if _, ok := tg.Get(2); ok {
		t.Error("a row the filter now rejects survived the re-baseline")
	}
	if tg.Count() != 1 || len(removed) != 1 || removed[0] != "b" {
		t.Errorf("count=%d removed=%v, want 1 and [b]", tg.Count(), removed)
	}
}
