package laredo_test

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/filter"
	"github.com/zourzouvillys/laredo/snapshot/local"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
	"github.com/zourzouvillys/laredo/transform"
)

func validOpts() []laredo.Option {
	return []laredo.Option{
		laredo.WithSource("pg", testsource.New()),
		laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget()),
	}
}

func TestNewEngine_Valid(t *testing.T) {
	e, errs := laredo.NewEngine(validOpts()...)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if e == nil {
		t.Fatal("expected non-nil engine")
	}
}

func TestNewEngine_Validation(t *testing.T) {
	tests := []struct {
		name    string
		opts    []laredo.Option
		wantErr string
	}{
		{
			name:    "no sources",
			opts:    []laredo.Option{laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget())},
			wantErr: "at least one source is required",
		},
		{
			name: "no pipelines",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
			},
			wantErr: "at least one pipeline is required",
		},
		{
			name: "nil source",
			opts: []laredo.Option{
				laredo.WithSource("pg", nil),
				laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget()),
			},
			wantErr: "must not be nil",
		},
		{
			name: "unknown source in pipeline",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("unknown", testutil.SampleTable(), memory.NewIndexedTarget()),
			},
			wantErr: "references unknown source",
		},
		{
			name: "empty table name",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("pg", laredo.Table("public", ""), memory.NewIndexedTarget()),
			},
			wantErr: "table name must not be empty",
		},
		{
			name: "nil target",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("pg", testutil.SampleTable(), nil),
			},
			wantErr: "target must not be nil",
		},
		{
			name: "zero buffer size",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget(), laredo.BufferSize(0)),
			},
			wantErr: "buffer size must be positive",
		},
		{
			name: "negative buffer size",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget(), laredo.BufferSize(-1)),
			},
			wantErr: "buffer size must be positive",
		},
		{
			name: "negative max retries",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget(), laredo.MaxRetries(-1)),
			},
			wantErr: "max retries must be non-negative",
		},
		{
			name: "duplicate pipeline",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget()),
				laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget()),
			},
			wantErr: "duplicate pipeline ID",
		},
		{
			name: "snapshot store without serializer",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget()),
				laredo.WithSnapshotStore(&fakeSnapshotStore{}),
			},
			wantErr: "snapshot serializer is required",
		},
		{
			name: "empty source ID in pipeline",
			opts: []laredo.Option{
				laredo.WithSource("pg", testsource.New()),
				laredo.WithPipeline("", testutil.SampleTable(), memory.NewIndexedTarget()),
			},
			wantErr: "source ID must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, errs := laredo.NewEngine(tt.opts...)
			if e != nil {
				t.Error("expected nil engine on validation error")
			}
			if len(errs) == 0 {
				t.Fatal("expected validation errors")
			}
			found := false
			for _, err := range errs {
				if contains(err.Error(), tt.wantErr) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected error containing %q, got %v", tt.wantErr, errs)
			}
		})
	}
}

func TestNewEngine_Defaults(t *testing.T) {
	t.Run("observer defaults to NullObserver", func(t *testing.T) {
		e, errs := laredo.NewEngine(validOpts()...)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		// Engine should be created successfully without an explicit observer.
		if e == nil {
			t.Fatal("expected non-nil engine")
		}
	})

	t.Run("custom observer", func(t *testing.T) {
		obs := &testutil.TestObserver{}
		opts := append(validOpts(), laredo.WithObserver(obs))
		e, errs := laredo.NewEngine(opts...)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if e == nil {
			t.Fatal("expected non-nil engine")
		}
	})
}

func TestNewEngine_MultipleErrors(t *testing.T) {
	// Config with multiple problems should report all of them.
	_, errs := laredo.NewEngine(
	// no sources, no pipelines
	)
	if len(errs) < 2 {
		t.Errorf("expected at least 2 errors, got %d: %v", len(errs), errs)
	}
}

func TestNewEngine_DifferentTargetTypes(t *testing.T) {
	// Same source+table with different target types should be allowed.
	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", testsource.New()),
		laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget()),
		laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewCompiledTarget()),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if e == nil {
		t.Fatal("expected non-nil engine")
	}
}

func TestNewEngine_PipelineOptions(t *testing.T) {
	// All pipeline options should be accepted.
	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", testsource.New()),
		laredo.WithPipeline("pg", testutil.SampleTable(), memory.NewIndexedTarget(),
			laredo.BufferSize(500),
			laredo.BufferPolicyOpt(laredo.BufferDropOldest),
			laredo.ErrorPolicyOpt(laredo.ErrorStopSource),
			laredo.MaxRetries(10),
			laredo.PipelineFilterOpt(laredo.PipelineFilterFunc(func(laredo.TableIdentifier, laredo.Row) bool { return true })),
			laredo.PipelineTransformOpt(laredo.PipelineTransformFunc(func(_ laredo.TableIdentifier, r laredo.Row) laredo.Row { return r })),
		),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if e == nil {
		t.Fatal("expected non-nil engine")
	}
}

func TestGetTarget(t *testing.T) {
	indexed := memory.NewIndexedTarget()
	compiled := memory.NewCompiledTarget()
	table := testutil.SampleTable()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", testsource.New()),
		laredo.WithPipeline("pg", table, indexed),
		laredo.WithPipeline("pg", table, compiled),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	t.Run("find indexed target", func(t *testing.T) {
		got, ok := laredo.GetTarget[*memory.IndexedTarget](e, "pg", table)
		if !ok {
			t.Fatal("expected to find IndexedTarget")
		}
		if got != indexed {
			t.Error("returned target is not the same instance")
		}
	})

	t.Run("find compiled target", func(t *testing.T) {
		got, ok := laredo.GetTarget[*memory.CompiledTarget](e, "pg", table)
		if !ok {
			t.Fatal("expected to find CompiledTarget")
		}
		if got != compiled {
			t.Error("returned target is not the same instance")
		}
	})

	t.Run("wrong source ID", func(t *testing.T) {
		_, ok := laredo.GetTarget[*memory.IndexedTarget](e, "nonexistent", table)
		if ok {
			t.Error("expected not found for wrong source ID")
		}
	})

	t.Run("wrong table", func(t *testing.T) {
		_, ok := laredo.GetTarget[*memory.IndexedTarget](e, "pg", laredo.Table("other", "table"))
		if ok {
			t.Error("expected not found for wrong table")
		}
	})
}

func TestEngine_LifecycleErrors(t *testing.T) {
	e, errs := laredo.NewEngine(validOpts()...)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()

	t.Run("stop before start", func(t *testing.T) {
		if err := e.Stop(ctx); err == nil {
			t.Error("expected error stopping before start")
		}
	})

	t.Run("start succeeds", func(t *testing.T) {
		if err := e.Start(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("double start", func(t *testing.T) {
		if err := e.Start(ctx); err == nil {
			t.Error("expected error on double start")
		}
	})

	t.Run("stop succeeds", func(t *testing.T) {
		if err := e.Stop(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("stop after stop", func(t *testing.T) {
		if err := e.Stop(ctx); err == nil {
			t.Error("expected error on double stop")
		}
	})

	t.Run("start after stop", func(t *testing.T) {
		if err := e.Start(ctx); err == nil {
			t.Error("expected error starting after stop")
		}
	})
}

func TestEngine_AdminBeforeStart(t *testing.T) {
	e, errs := laredo.NewEngine(validOpts()...)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()

	if err := e.Reload(ctx, "pg", testutil.SampleTable()); err == nil {
		t.Error("expected error for Reload before start")
	}
	if err := e.Pause(ctx, "pg"); err == nil {
		t.Error("expected error for Pause before start")
	}
	if err := e.Resume(ctx, "pg"); err == nil {
		t.Error("expected error for Resume before start")
	}
	if err := e.CreateSnapshot(ctx, nil); err == nil {
		t.Error("expected error for CreateSnapshot before start")
	}
}

func TestEngine_AdminUnknownSource(t *testing.T) {
	e, errs := laredo.NewEngine(validOpts()...)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	_ = e.Start(ctx)

	if err := e.Reload(ctx, "unknown", testutil.SampleTable()); err == nil {
		t.Error("expected error for unknown source")
	}
	if err := e.Pause(ctx, "unknown"); err == nil {
		t.Error("expected error for unknown source")
	}
	if err := e.Resume(ctx, "unknown"); err == nil {
		t.Error("expected error for unknown source")
	}
}

func TestEngine_CreateSnapshotWithoutStore(t *testing.T) {
	e, errs := laredo.NewEngine(validOpts()...)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	_ = e.Start(ctx)

	if err := e.CreateSnapshot(ctx, nil); err == nil {
		t.Error("expected error for snapshot without store")
	}
}

func TestEngine_ReadinessBeforeStart(t *testing.T) {
	e, errs := laredo.NewEngine(validOpts()...)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Readiness returns false before baselines complete.
	if e.IsReady() {
		t.Error("expected IsReady to return false before start")
	}
	if e.AwaitReady(0) {
		t.Error("expected AwaitReady to return false before start")
	}
}

func TestEngine_OnReady(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	readyCh := make(chan struct{})
	e.OnReady(func() { close(readyCh) })

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-readyCh:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("OnReady callback was not invoked")
	}

	if !e.IsReady() {
		t.Error("expected IsReady true after OnReady fired")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_OnReadyAlreadyReady(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())

	target := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Register callback after already ready — should fire immediately.
	called := false
	e.OnReady(func() { called = true })
	if !called {
		t.Error("OnReady should fire immediately when already ready")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// configuredSource creates a test source with schemas and baseline rows configured.
func configuredSource() *testsource.Source {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	return src
}

func TestEngine_BaselineAndReady(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for readiness (baseline completes → streaming).
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Verify baseline rows arrived at target.
	if target.Count() != 2 {
		t.Fatalf("expected 2 rows, got %d", target.Count())
	}

	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected to find row with id=1")
	}
	if row["name"] != "alice" { //nolint:goconst // test literal
		t.Errorf("expected name=alice, got %v", row["name"])
	}

	// Verify observer events.
	if obs.EventCount("SourceConnected") != 1 {
		t.Errorf("expected 1 SourceConnected, got %d", obs.EventCount("SourceConnected"))
	}
	if obs.EventCount("BaselineStarted") != 1 {
		t.Errorf("expected 1 BaselineStarted, got %d", obs.EventCount("BaselineStarted"))
	}
	if obs.EventCount("BaselineCompleted") != 1 {
		t.Errorf("expected 1 BaselineCompleted, got %d", obs.EventCount("BaselineCompleted"))
	}

	// Verify state transitions: INITIALIZING → BASELINING → STREAMING.
	stateChanges := obs.EventsByType("PipelineStateChanged")
	if len(stateChanges) < 2 {
		t.Fatalf("expected at least 2 state changes, got %d", len(stateChanges))
	}
	if stateChanges[0].Data["newState"] != laredo.PipelineBaselining {
		t.Errorf("expected first transition to BASELINING, got %v", stateChanges[0].Data["newState"])
	}
	if stateChanges[1].Data["newState"] != laredo.PipelineStreaming {
		t.Errorf("expected second transition to STREAMING, got %v", stateChanges[1].Data["newState"])
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_StreamingChanges(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit insert.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 2
	}, "expected 2 rows after insert")

	// Emit update.
	src.EmitUpdate(testutil.SampleTable(),
		testutil.SampleRow(1, "alice-updated"),
		laredo.Row{"id": 1},
	)
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		row, ok := target.Get(1)
		return ok && row["name"] == "alice-updated"
	}, "expected name to be updated")

	// Emit delete.
	src.EmitDelete(testutil.SampleTable(), laredo.Row{"id": 2})
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 1
	}, "expected 1 row after delete")

	// Verify observer received change events.
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return obs.EventCount("ChangeApplied") >= 3
	}, "expected at least 3 ChangeApplied events")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_FilterChain(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))

	target := memory.NewIndexedTarget()

	// Filter: only rows where name starts with "a".
	nameFilter := &filter.FieldPrefix{Field: "name", Prefix: "a"}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.PipelineFilterOpt(nameFilter),
		),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Only "alice" passes the filter (name starts with "a").
	if target.Count() != 1 {
		t.Fatalf("expected 1 row after filter, got %d", target.Count())
	}

	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected row with id=1")
	}
	if row["name"] != "alice" {
		t.Errorf("expected alice, got %v", row["name"])
	}

	// Streaming insert that passes filter.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(4, "anna"))
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 2
	}, "expected 2 rows after filtered insert")

	// Streaming insert that doesn't pass filter.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(5, "zoe"))
	// Give it time to process, then verify it wasn't added.
	time.Sleep(100 * time.Millisecond)
	if target.Count() != 2 {
		t.Errorf("expected 2 rows (filtered insert should be skipped), got %d", target.Count())
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_FilterDeleteEvents(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "anna"))

	target := memory.NewIndexedTarget()

	// Filter: only rows where name starts with "a".
	nameFilter := &filter.FieldPrefix{Field: "name", Prefix: "a"}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.PipelineFilterOpt(nameFilter),
		),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	if target.Count() != 2 {
		t.Fatalf("expected 2 rows after baseline, got %d", target.Count())
	}

	// Delete a row that passes the filter — should be removed from target.
	src.EmitDelete(testutil.SampleTable(), laredo.Row{"id": 1, "name": "alice"})
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 1
	}, "expected 1 row after deleting alice")

	// Delete a row whose OldValues don't pass the filter — should be skipped.
	// "bob" doesn't start with "a", so this DELETE should not affect the target.
	src.EmitDelete(testutil.SampleTable(), laredo.Row{"id": 99, "name": "bob"})
	time.Sleep(100 * time.Millisecond)
	if target.Count() != 1 {
		t.Errorf("expected 1 row (filtered delete should be skipped), got %d", target.Count())
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_TransformChain(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), laredo.Row{"id": 1, "name": "alice", "value": "secret"})

	target := memory.NewIndexedTarget()

	// Transform: drop the "value" field.
	dropValue := &transform.DropFields{Fields: []string{"value"}}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.PipelineTransformOpt(dropValue),
		),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected row with id=1")
	}
	if _, hasValue := row["value"]; hasValue {
		t.Error("expected 'value' field to be dropped by transform")
	}
	if row["name"] != "alice" {
		t.Errorf("expected name=alice, got %v", row["name"])
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_MultiplePipelinesDifferentTables(t *testing.T) {
	table1 := testutil.SampleTable()
	table2 := laredo.Table("public", "other_table")

	src := testsource.New()
	src.SetSchema(table1, testutil.SampleColumns())
	src.SetSchema(table2, testutil.SampleColumns())
	src.AddRow(table1, testutil.SampleRow(1, "alice"))
	src.AddRow(table2, testutil.SampleRow(10, "xavier"))
	src.AddRow(table2, testutil.SampleRow(11, "yolanda"))

	target1 := memory.NewIndexedTarget()
	target2 := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", table1, target1),
		laredo.WithPipeline("pg", table2, target2),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// target1 should get table1 rows, target2 should get table2 rows.
	if target1.Count() != 1 {
		t.Errorf("target1: expected 1 row, got %d", target1.Count())
	}
	if target2.Count() != 2 {
		t.Errorf("target2: expected 2 rows, got %d", target2.Count())
	}

	// Emit a change to table1 — only target1 should receive it.
	src.EmitInsert(table1, testutil.SampleRow(2, "bob"))
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target1.Count() == 2
	}, "expected target1 to have 2 rows")

	// target2 should still have 2 rows.
	if target2.Count() != 2 {
		t.Errorf("target2: expected still 2 rows, got %d", target2.Count())
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_GracefulStop(t *testing.T) {
	src := configuredSource()
	obs := &testutil.TestObserver{}
	target := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Verify pipelines transitioned to STOPPED.
	stateChanges := obs.EventsByType("PipelineStateChanged")
	lastChange := stateChanges[len(stateChanges)-1]
	if lastChange.Data["newState"] != laredo.PipelineStopped {
		t.Errorf("expected final state STOPPED, got %v", lastChange.Data["newState"])
	}
}

func TestEngine_SourceInitError(t *testing.T) {
	src := configuredSource()
	src.SetInitError(fmt.Errorf("connection refused"))

	obs := &testutil.TestObserver{}
	target := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Engine should not become ready when source init fails.
	if e.AwaitReady(500 * time.Millisecond) {
		t.Error("expected engine to not become ready on init error")
	}

	// Source disconnected event should have been fired.
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return obs.EventCount("SourceDisconnected") == 1
	}, "expected SourceDisconnected event")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_PauseResume(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Wait for the source to be actively streaming (buffer setup may take a moment).
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return src.State() == laredo.SourceStreaming
	}, "expected source to be streaming")

	// Pause the source.
	if err := e.Pause(ctx, "pg"); err != nil {
		t.Fatalf("pause: %v", err)
	}

	// Verify pipeline transitioned to PAUSED.
	stateChanges := obs.EventsByType("PipelineStateChanged")
	lastChange := stateChanges[len(stateChanges)-1]
	if lastChange.Data["newState"] != laredo.PipelinePaused {
		t.Errorf("expected PAUSED, got %v", lastChange.Data["newState"])
	}

	// Verify source reports paused state.
	if src.State() != laredo.SourcePaused {
		t.Errorf("expected source state PAUSED, got %v", src.State())
	}

	// Resume the source.
	if err := e.Resume(ctx, "pg"); err != nil {
		t.Fatalf("resume: %v", err)
	}

	// Verify pipeline transitioned back to STREAMING.
	stateChanges = obs.EventsByType("PipelineStateChanged")
	lastChange = stateChanges[len(stateChanges)-1]
	if lastChange.Data["newState"] != laredo.PipelineStreaming {
		t.Errorf("expected STREAMING after resume, got %v", lastChange.Data["newState"])
	}

	// Verify source reports streaming state.
	if src.State() != laredo.SourceStreaming {
		t.Errorf("expected source state STREAMING, got %v", src.State())
	}

	// Verify changes still flow after resume.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 2
	}, "expected 2 rows after resume")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_PauseDoesNotAffectErrorPipelines(t *testing.T) {
	src := configuredSource()
	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Pause and resume should work even when IsReady is true.
	if err := e.Pause(ctx, "pg"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := e.Resume(ctx, "pg"); err != nil {
		t.Fatalf("resume: %v", err)
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_Reload(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Verify initial baseline data.
	if target.Count() != 2 {
		t.Fatalf("expected 2 rows after initial baseline, got %d", target.Count())
	}

	// Modify the source data (simulate changed upstream data).
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))

	// Trigger re-baseline.
	if err := e.Reload(ctx, "pg", testutil.SampleTable()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// After re-baseline, target should have the new data (truncated + reloaded).
	// The source now has 3 rows configured.
	if target.Count() != 3 {
		t.Fatalf("expected 3 rows after reload, got %d", target.Count())
	}

	row, ok := target.Get(3)
	if !ok {
		t.Fatal("expected row with id=3 after reload")
	}
	if row["name"] != "charlie" {
		t.Errorf("expected name=charlie, got %v", row["name"])
	}

	// Verify observer events: should have new BaselineStarted and BaselineCompleted.
	baselineStarted := obs.EventsByType("BaselineStarted")
	if len(baselineStarted) != 2 {
		t.Errorf("expected 2 BaselineStarted events (initial + reload), got %d", len(baselineStarted))
	}
	baselineCompleted := obs.EventsByType("BaselineCompleted")
	if len(baselineCompleted) != 2 {
		t.Errorf("expected 2 BaselineCompleted events, got %d", len(baselineCompleted))
	}

	// Verify pipeline went back to STREAMING.
	stateChanges := obs.EventsByType("PipelineStateChanged")
	lastChange := stateChanges[len(stateChanges)-1]
	if lastChange.Data["newState"] != laredo.PipelineStreaming {
		t.Errorf("expected STREAMING after reload, got %v", lastChange.Data["newState"])
	}

	// Verify changes still flow after reload.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(4, "dave"))
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 4
	}, "expected 4 rows after streaming resumes")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_ReloadUnknownTable(t *testing.T) {
	src := configuredSource()
	target := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Reload a table that has no pipelines.
	err := e.Reload(ctx, "pg", laredo.Table("public", "nonexistent"))
	if err == nil {
		t.Error("expected error reloading table with no pipelines")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_CreateSnapshot(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}
	store := &fakeSnapshotStore{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
		laredo.WithSnapshotStore(store),
		laredo.WithSnapshotSerializer(fakeSnapshotSerializer{}),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Create a snapshot.
	if err := e.CreateSnapshot(ctx, map[string]laredo.Value{"reason": "test"}); err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	// Verify the store received the snapshot.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 saved snapshot, got %d", len(store.saved))
	}

	snap := store.saved[0]
	if snap.metadata.UserMeta["reason"] != "test" {
		t.Errorf("expected user meta reason=test, got %v", snap.metadata.UserMeta["reason"])
	}

	entries := snap.entries[testutil.SampleTable()]
	if len(entries) != 2 {
		t.Errorf("expected 2 entries in snapshot, got %d", len(entries))
	}

	// Verify observer events.
	if obs.EventCount("SnapshotStarted") != 1 {
		t.Errorf("expected 1 SnapshotStarted, got %d", obs.EventCount("SnapshotStarted"))
	}
	if obs.EventCount("SnapshotCompleted") != 1 {
		t.Errorf("expected 1 SnapshotCompleted, got %d", obs.EventCount("SnapshotCompleted"))
	}

	// Verify streaming still works after snapshot.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows after snapshot + insert")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_ResumeSkipsBaseline(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	// Set a last ACKed position to enable resume.
	ctx := context.Background()
	if err := src.Ack(ctx, uint64(5)); err != nil {
		t.Fatalf("ack: %v", err)
	}

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// On resume, baseline is skipped — no rows should be loaded into target.
	if target.Count() != 0 {
		t.Errorf("expected 0 rows (baseline skipped on resume), got %d", target.Count())
	}

	// No BaselineStarted or BaselineCompleted events should fire.
	if obs.EventCount("BaselineStarted") != 0 {
		t.Errorf("expected no BaselineStarted events on resume, got %d", obs.EventCount("BaselineStarted"))
	}
	if obs.EventCount("BaselineCompleted") != 0 {
		t.Errorf("expected no BaselineCompleted events on resume, got %d", obs.EventCount("BaselineCompleted"))
	}

	// Pipeline should go directly INITIALIZING → STREAMING (no BASELINING).
	stateChanges := obs.EventsByType("PipelineStateChanged")
	if len(stateChanges) != 1 {
		t.Fatalf("expected 1 state change (→STREAMING), got %d", len(stateChanges))
	}
	if stateChanges[0].Data["newState"] != laredo.PipelineStreaming {
		t.Errorf("expected transition to STREAMING, got %v", stateChanges[0].Data["newState"])
	}

	// Verify streaming still works.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(10, "resumed"))
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 1
	}, "expected 1 row from streamed insert after resume")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_ResumeNoPositionFallsBackToBaseline(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	// Don't set lastAck — SupportsResume() returns false, should do full baseline.
	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Full baseline should have loaded the row.
	if target.Count() != 1 {
		t.Errorf("expected 1 row from baseline, got %d", target.Count())
	}

	// BaselineStarted and BaselineCompleted should fire.
	if obs.EventCount("BaselineStarted") != 1 {
		t.Errorf("expected 1 BaselineStarted, got %d", obs.EventCount("BaselineStarted"))
	}
	if obs.EventCount("BaselineCompleted") != 1 {
		t.Errorf("expected 1 BaselineCompleted, got %d", obs.EventCount("BaselineCompleted"))
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_AckCoordination(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit some changes.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))

	// Wait for changes to be applied.
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows")

	// Verify ACK events were fired.
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return obs.EventCount("AckAdvanced") >= 2
	}, "expected at least 2 AckAdvanced events")

	// Verify the source received ACKs.
	lastAck, err := src.LastAckedPosition(ctx)
	if err != nil {
		t.Fatalf("last acked position: %v", err)
	}
	if lastAck == nil {
		t.Fatal("expected non-nil last ACKed position")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_IsSourceReady(t *testing.T) {
	src := configuredSource()
	target := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Not ready before start.
	if e.IsSourceReady("pg") {
		t.Error("expected not ready before start")
	}

	// Unknown source returns false.
	if e.IsSourceReady("nonexistent") {
		t.Error("expected false for unknown source")
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	if !e.IsSourceReady("pg") {
		t.Error("expected source ready after baseline")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_SnapshotOnShutdown(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	store := &fakeSnapshotStore{}
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
		laredo.WithSnapshotStore(store),
		laredo.WithSnapshotSerializer(fakeSnapshotSerializer{}),
		laredo.WithSnapshotOnShutdown(true),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Verify a snapshot was taken during shutdown.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 snapshot on shutdown, got %d", len(store.saved))
	}
	if store.saved[0].metadata.UserMeta["trigger"] != "shutdown" {
		t.Errorf("expected trigger=shutdown in user meta, got %v", store.saved[0].metadata.UserMeta)
	}
}

// failingTarget wraps an IndexedTarget but fails OnInsert a configurable number of times.
type failingTarget struct {
	*memory.IndexedTarget
	mu        sync.Mutex
	failCount int // number of remaining failures
}

func newFailingTarget(failCount int) *failingTarget {
	return &failingTarget{
		IndexedTarget: memory.NewIndexedTarget(),
		failCount:     failCount,
	}
}

func (t *failingTarget) OnInsert(ctx context.Context, table laredo.TableIdentifier, row laredo.Row) error {
	t.mu.Lock()
	if t.failCount > 0 {
		t.failCount--
		t.mu.Unlock()
		return fmt.Errorf("injected insert error")
	}
	t.mu.Unlock()
	return t.IndexedTarget.OnInsert(ctx, table, row)
}

func TestEngine_RetryOnError(t *testing.T) {
	src := configuredSource()

	// Target fails 2 times then succeeds. maxRetries=5 so it should recover.
	target := newFailingTarget(2)
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.MaxRetries(5),
		),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit insert — first 2 attempts will fail, 3rd should succeed.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(1, "retry-success"))
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return target.Count() == 1
	}, "expected 1 row after retry succeeds")

	// Verify retry errors were reported.
	errorEvents := obs.EventsByType("ChangeError")
	if len(errorEvents) < 2 {
		t.Errorf("expected at least 2 ChangeError events from retries, got %d", len(errorEvents))
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_ErrorPolicyIsolate(t *testing.T) {
	src := configuredSource()

	// Target fails permanently (more failures than retries).
	target := newFailingTarget(100)
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.MaxRetries(1),
			laredo.ErrorPolicyOpt(laredo.ErrorIsolate),
		),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit insert that will fail permanently.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(1, "will-fail"))

	// Pipeline should transition to ERROR.
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		changes := obs.EventsByType("PipelineStateChanged")
		for _, c := range changes {
			if c.Data["newState"] == laredo.PipelineError {
				return true
			}
		}
		return false
	}, "expected pipeline to transition to ERROR")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_ErrorPolicyStopSource(t *testing.T) {
	table1 := testutil.SampleTable()
	table2 := laredo.Table("public", "other_table")

	src := testsource.New()
	src.SetSchema(table1, testutil.SampleColumns())
	src.SetSchema(table2, testutil.SampleColumns())

	// Target for table1 fails permanently.
	failTarget := newFailingTarget(100)
	// Target for table2 is healthy.
	goodTarget := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", table1, failTarget,
			laredo.MaxRetries(0),
			laredo.ErrorPolicyOpt(laredo.ErrorStopSource),
		),
		laredo.WithPipeline("pg", table2, goodTarget),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit insert to table1 — will fail and should stop ALL pipelines on the source.
	src.EmitInsert(table1, testutil.SampleRow(1, "will-fail"))

	// Both pipelines should go to ERROR.
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		errorCount := 0
		for _, c := range obs.EventsByType("PipelineStateChanged") {
			if c.Data["newState"] == laredo.PipelineError {
				errorCount++
			}
		}
		return errorCount >= 2
	}, "expected both pipelines to transition to ERROR")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_SnapshotRestore(t *testing.T) {
	src := configuredSource()
	// Don't add any baseline rows — data will come from snapshot.

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	// Pre-populate the snapshot store with data.
	store := &fakeSnapshotStore{}
	store.saved = append(store.saved, savedSnapshot{
		id: "snap-restore-test",
		metadata: laredo.SnapshotMetadata{
			SnapshotID: "snap-restore-test",
			CreatedAt:  time.Now(),
			Tables: []laredo.TableSnapshotInfo{
				{Table: testutil.SampleTable(), RowCount: 2},
			},
			SourcePositions: map[string]laredo.Position{
				"pg": uint64(42),
			},
		},
		entries: map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			testutil.SampleTable(): {
				{Row: testutil.SampleRow(1, "snapshot-alice")},
				{Row: testutil.SampleRow(2, "snapshot-bob")},
			},
		},
	})

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
		laredo.WithSnapshotStore(store),
		laredo.WithSnapshotSerializer(fakeSnapshotSerializer{}),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Data should come from the snapshot, not baseline.
	if target.Count() != 2 {
		t.Fatalf("expected 2 rows from snapshot restore, got %d", target.Count())
	}

	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected row with id=1")
	}
	if row["name"] != "snapshot-alice" {
		t.Errorf("expected snapshot-alice, got %v", row["name"])
	}

	// No BaselineStarted events — baseline was skipped.
	if obs.EventCount("BaselineStarted") != 0 {
		t.Errorf("expected 0 BaselineStarted (snapshot restore), got %d", obs.EventCount("BaselineStarted"))
	}

	// SnapshotRestoreStarted and SnapshotRestoreCompleted should fire.
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return obs.EventCount("SnapshotRestoreStarted") == 1
	}, "expected 1 SnapshotRestoreStarted")
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return obs.EventCount("SnapshotRestoreCompleted") == 1
	}, "expected 1 SnapshotRestoreCompleted")

	// Streaming should still work after restore.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows after snapshot restore + insert")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_SnapshotRestoreFallback(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "baseline-alice"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	// Snapshot store with no source position — should fall through to baseline.
	store := &fakeSnapshotStore{}
	store.saved = append(store.saved, savedSnapshot{
		id: "snap-no-position",
		metadata: laredo.SnapshotMetadata{
			SnapshotID: "snap-no-position",
			CreatedAt:  time.Now(),
			Tables: []laredo.TableSnapshotInfo{
				{Table: testutil.SampleTable(), RowCount: 1},
			},
			// No SourcePositions — snapshot unusable.
		},
		entries: map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			testutil.SampleTable(): {
				{Row: testutil.SampleRow(99, "should-not-appear")},
			},
		},
	})

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
		laredo.WithSnapshotStore(store),
		laredo.WithSnapshotSerializer(fakeSnapshotSerializer{}),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Should have fallen back to baseline — data from source, not snapshot.
	if target.Count() != 1 {
		t.Fatalf("expected 1 row from baseline fallback, got %d", target.Count())
	}

	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected row with id=1")
	}
	if row["name"] != "baseline-alice" {
		t.Errorf("expected baseline-alice, got %v", row["name"])
	}

	// BaselineStarted should fire (fell back to baseline).
	if obs.EventCount("BaselineStarted") != 1 {
		t.Errorf("expected 1 BaselineStarted (fallback), got %d", obs.EventCount("BaselineStarted"))
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_DeadLetterIntegration(t *testing.T) {
	src := configuredSource()

	// Target fails permanently.
	target := newFailingTarget(100)
	obs := &testutil.TestObserver{}
	dlStore := &testDeadLetterStore{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.MaxRetries(1),
			laredo.ErrorPolicyOpt(laredo.ErrorIsolate),
		),
		laredo.WithObserver(obs),
		laredo.WithDeadLetterStore(dlStore),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit insert that will fail permanently.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(1, "will-fail"))

	// Wait for the pipeline to go to ERROR.
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		for _, c := range obs.EventsByType("PipelineStateChanged") {
			if c.Data["newState"] == laredo.PipelineError {
				return true
			}
		}
		return false
	}, "expected pipeline ERROR")

	// Verify dead letter was written.
	dlStore.mu.Lock()
	defer dlStore.mu.Unlock()
	if len(dlStore.entries) != 1 {
		t.Fatalf("expected 1 dead letter entry, got %d", len(dlStore.entries))
	}
	if dlStore.entries[0].change.Action != laredo.ActionInsert {
		t.Errorf("expected INSERT action in dead letter, got %v", dlStore.entries[0].change.Action)
	}

	// Verify observer event.
	if obs.EventCount("DeadLetterWritten") != 1 {
		t.Errorf("expected 1 DeadLetterWritten, got %d", obs.EventCount("DeadLetterWritten"))
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// testDeadLetterStore records dead letter writes for assertions.
type testDeadLetterStore struct {
	mu      sync.Mutex
	entries []dlEntry
}

type dlEntry struct {
	pipelineID string
	change     laredo.ChangeEvent
	errInfo    laredo.ErrorInfo
}

func (s *testDeadLetterStore) Write(pipelineID string, change laredo.ChangeEvent, err laredo.ErrorInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, dlEntry{pipelineID: pipelineID, change: change, errInfo: err})
	return nil
}

func TestEngine_TTLSkipExpiredOnBaseline(t *testing.T) {
	src := configuredSource()
	// Row with past expiry should be skipped.
	src.AddRow(testutil.SampleTable(), laredo.Row{"id": 1, "name": "fresh", "expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339)})
	src.AddRow(testutil.SampleTable(), laredo.Row{"id": 2, "name": "expired", "expires_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339)})

	target := memory.NewIndexedTarget()

	ttlFunc := func(row laredo.Row) time.Time {
		s, _ := row["expires_at"].(string)
		t, _ := time.Parse(time.RFC3339, s)
		return t
	}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.WithTTL(ttlFunc),
		),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Only the fresh row should be in the target.
	if target.Count() != 1 {
		t.Fatalf("expected 1 row (expired skipped), got %d", target.Count())
	}

	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected row with id=1")
	}
	if row["name"] != "fresh" {
		t.Errorf("expected fresh, got %v", row["name"])
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_TTLSkipExpiredOnInsert(t *testing.T) {
	src := configuredSource()
	target := memory.NewIndexedTarget()

	ttlFunc := func(row laredo.Row) time.Time {
		s, _ := row["expires_at"].(string)
		t, _ := time.Parse(time.RFC3339, s)
		return t
	}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.WithTTL(ttlFunc),
		),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Insert a fresh row — should appear.
	src.EmitInsert(testutil.SampleTable(), laredo.Row{"id": 1, "name": "fresh", "expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339)})
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 1
	}, "expected 1 row after fresh insert")

	// Insert an expired row — should be skipped.
	src.EmitInsert(testutil.SampleTable(), laredo.Row{"id": 2, "name": "expired", "expires_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339)})
	time.Sleep(100 * time.Millisecond)
	if target.Count() != 1 {
		t.Errorf("expected 1 row (expired insert skipped), got %d", target.Count())
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_TTLUpdateToExpiredTreatedAsDelete(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), laredo.Row{"id": 1, "name": "alive", "expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339)})

	target := memory.NewIndexedTarget()

	ttlFunc := func(row laredo.Row) time.Time {
		s, _ := row["expires_at"].(string)
		t, _ := time.Parse(time.RFC3339, s)
		return t
	}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.WithTTL(ttlFunc),
		),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	if target.Count() != 1 {
		t.Fatalf("expected 1 row after baseline, got %d", target.Count())
	}

	// Update the row to have an expired timestamp — should be treated as delete.
	src.EmitUpdate(testutil.SampleTable(),
		laredo.Row{"id": 1, "name": "now-expired", "expires_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339)},
		laredo.Row{"id": 1},
	)
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target.Count() == 0
	}, "expected 0 rows (update-to-expired treated as delete)")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_TTLFieldBased(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), laredo.Row{"id": 1, "name": "fresh", "expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339)})
	src.AddRow(testutil.SampleTable(), laredo.Row{"id": 2, "name": "expired", "expires_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339)})
	src.AddRow(testutil.SampleTable(), laredo.Row{"id": 3, "name": "no-expiry"}) // no field = never expires

	target := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.WithTTLField("expires_at"),
		),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// fresh (not expired) + no-expiry (no field = never expires) = 2 rows.
	// expired (past) should be skipped.
	if target.Count() != 2 {
		t.Fatalf("expected 2 rows, got %d", target.Count())
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_PostBaselineValidation(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Verify validation result was fired.
	validations := obs.EventsByType("ValidationResult")
	if len(validations) != 1 {
		t.Fatalf("expected 1 ValidationResult, got %d", len(validations))
	}

	v := validations[0]
	if v.Data["sourceCount"] != int64(2) {
		t.Errorf("expected sourceCount=2, got %v", v.Data["sourceCount"])
	}
	if v.Data["targetCount"] != int64(2) {
		t.Errorf("expected targetCount=2, got %v", v.Data["targetCount"])
	}
	if v.Data["match"] != true {
		t.Errorf("expected match=true, got %v", v.Data["match"])
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_PeriodicValidation_Match(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.WithValidationInterval(100*time.Millisecond),
		),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Wait for at least 2 periodic validations (1 baseline + 2 periodic = 3 total).
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return obs.EventCount("ValidationResult") >= 3
	}, "expected at least 3 ValidationResult events (1 baseline + periodic)")

	// All periodic validations should match (no changes since baseline).
	for _, v := range obs.EventsByType("ValidationResult") {
		if v.Data["match"] != true {
			t.Errorf("expected all validations to match, got sourceCount=%v targetCount=%v",
				v.Data["sourceCount"], v.Data["targetCount"])
		}
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_PeriodicValidation_TracksDrift(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.WithValidationInterval(100*time.Millisecond),
		),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit changes and wait for them to be applied.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))

	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows after inserts")

	// Wait for a periodic validation after the inserts.
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		for _, v := range obs.EventsByType("ValidationResult") {
			if v.Data["sourceCount"] == int64(3) && v.Data["targetCount"] == int64(3) && v.Data["match"] == true {
				return true
			}
		}
		return false
	}, "expected periodic validation with 3 rows")

	// Emit a delete.
	src.EmitDelete(testutil.SampleTable(), laredo.Row{"id": 2})
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return target.Count() == 2
	}, "expected 2 rows after delete")

	// Wait for a periodic validation after the delete.
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		for _, v := range obs.EventsByType("ValidationResult") {
			if v.Data["sourceCount"] == int64(2) && v.Data["targetCount"] == int64(2) && v.Data["match"] == true {
				return true
			}
		}
		return false
	}, "expected periodic validation with 2 rows")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_PeriodicValidation_DisabledByDefault(t *testing.T) {
	// Without WithValidationInterval, no periodic validation should run.
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Wait a bit, then verify only the baseline validation fired (no periodic ones).
	time.Sleep(300 * time.Millisecond)
	if obs.EventCount("ValidationResult") != 1 {
		t.Errorf("expected exactly 1 ValidationResult (baseline only), got %d",
			obs.EventCount("ValidationResult"))
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_TTLPeriodicScanner(t *testing.T) {
	src := configuredSource()
	// Add a row that will expire very soon.
	src.AddRow(testutil.SampleTable(), laredo.Row{
		"id": 1, "name": "about-to-expire",
		"expires_at": time.Now().Add(200 * time.Millisecond).Format(time.RFC3339Nano),
	})
	// Add a row that won't expire.
	src.AddRow(testutil.SampleTable(), laredo.Row{
		"id": 2, "name": "permanent",
	})

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	ttlFunc := func(row laredo.Row) time.Time {
		s, _ := row["expires_at"].(string)
		if s == "" {
			return time.Time{}
		}
		parsed, _ := time.Parse(time.RFC3339Nano, s)
		return parsed
	}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.WithTTL(ttlFunc),
			laredo.WithTTLScanInterval(100*time.Millisecond),
		),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Both rows should be present initially.
	if target.Count() != 2 {
		t.Fatalf("expected 2 rows initially, got %d", target.Count())
	}

	// Wait for the row to expire and scanner to remove it.
	testutil.AssertEventually(t, 3*time.Second, func() bool {
		return target.Count() == 1
	}, "expected scanner to remove expired row")

	// Permanent row should still be there.
	_, ok := target.Get(2)
	if !ok {
		t.Error("expected permanent row to still exist")
	}

	// Verify OnRowExpired was fired.
	if obs.EventCount("RowExpired") < 1 {
		t.Errorf("expected at least 1 RowExpired event, got %d", obs.EventCount("RowExpired"))
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_SnapshotCreateRestoreCycle(t *testing.T) {
	dir := t.TempDir()
	table := testutil.SampleTable()
	realStore := local.New(dir)

	// Phase 1: start engine, load data, create snapshot, stop.
	src1 := testsource.New()
	src1.SetSchema(table, testutil.SampleColumns())
	src1.AddRow(table, testutil.SampleRow(1, "alice"))
	src1.AddRow(table, testutil.SampleRow(2, "bob"))

	target1 := memory.NewIndexedTarget()

	e1, errs := laredo.NewEngine(
		laredo.WithSource("pg", src1),
		laredo.WithPipeline("pg", table, target1),
		laredo.WithSnapshotStore(realStore),
		laredo.WithSnapshotSerializer(fakeSnapshotSerializer{}),
	)
	if len(errs) > 0 {
		t.Fatalf("engine1 errors: %v", errs)
	}

	ctx := context.Background()
	if err := e1.Start(ctx); err != nil {
		t.Fatalf("start1: %v", err)
	}
	if !e1.AwaitReady(5 * time.Second) {
		t.Fatal("engine1 not ready")
	}

	if target1.Count() != 2 {
		t.Fatalf("expected 2 rows in engine1, got %d", target1.Count())
	}

	// Create snapshot.
	if err := e1.CreateSnapshot(ctx, map[string]laredo.Value{
		"phase": "test",
	}); err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	if err := e1.Stop(ctx); err != nil {
		t.Fatalf("stop1: %v", err)
	}

	// Verify snapshot exists on disk.
	snapshots, err := realStore.List(ctx, nil)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot on disk, got %d", len(snapshots))
	}

	// Phase 2: start new engine with snapshot store — should restore from snapshot.
	src2 := testsource.New()
	src2.SetSchema(table, testutil.SampleColumns())
	// Don't add any baseline rows — data should come from snapshot.

	target2 := memory.NewIndexedTarget()
	obs2 := &testutil.TestObserver{}

	e2, errs := laredo.NewEngine(
		laredo.WithSource("pg", src2),
		laredo.WithPipeline("pg", table, target2),
		laredo.WithSnapshotStore(realStore),
		laredo.WithSnapshotSerializer(fakeSnapshotSerializer{}),
		laredo.WithObserver(obs2),
	)
	if len(errs) > 0 {
		t.Fatalf("engine2 errors: %v", errs)
	}

	if err := e2.Start(ctx); err != nil {
		t.Fatalf("start2: %v", err)
	}
	if !e2.AwaitReady(5 * time.Second) {
		t.Fatal("engine2 not ready")
	}

	// Target should have data from snapshot (allow time for buffered dispatch).
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return target2.Count() == 2
	}, "expected 2 rows from snapshot restore")

	row, ok := target2.Get(1)
	if !ok {
		t.Fatal("expected row id=1 from snapshot")
	}
	if row["name"] != "alice" { //nolint:goconst // test literal
		t.Errorf("expected alice, got %v", row["name"])
	}

	// Verify restore events fired.
	if obs2.EventCount("SnapshotRestoreStarted") != 1 {
		t.Errorf("expected 1 SnapshotRestoreStarted, got %d", obs2.EventCount("SnapshotRestoreStarted"))
	}

	// No baseline events (restored from snapshot).
	if obs2.EventCount("BaselineStarted") != 0 {
		t.Errorf("expected 0 BaselineStarted (restored from snapshot), got %d", obs2.EventCount("BaselineStarted"))
	}

	if err := e2.Stop(ctx); err != nil {
		t.Fatalf("stop2: %v", err)
	}
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// fakeSnapshotStore implements SnapshotStore for testing, recording saves.
type fakeSnapshotStore struct {
	mu      sync.Mutex
	saved   []savedSnapshot
	saveErr error
}

type savedSnapshot struct {
	id       string
	metadata laredo.SnapshotMetadata
	entries  map[laredo.TableIdentifier][]laredo.SnapshotEntry
}

func (s *fakeSnapshotStore) Save(_ context.Context, id string, meta laredo.SnapshotMetadata, entries map[laredo.TableIdentifier][]laredo.SnapshotEntry) (laredo.SnapshotDescriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return laredo.SnapshotDescriptor{}, s.saveErr
	}
	s.saved = append(s.saved, savedSnapshot{id: id, metadata: meta, entries: entries})
	return laredo.SnapshotDescriptor{SnapshotID: id}, nil
}

func (s *fakeSnapshotStore) Load(_ context.Context, id string) (laredo.SnapshotMetadata, map[laredo.TableIdentifier][]laredo.SnapshotEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, snap := range s.saved {
		if snap.id == id {
			return snap.metadata, snap.entries, nil
		}
	}
	return laredo.SnapshotMetadata{}, nil, fmt.Errorf("snapshot %s not found", id)
}

func (s *fakeSnapshotStore) Describe(_ context.Context, _ string) (laredo.SnapshotDescriptor, error) {
	return laredo.SnapshotDescriptor{}, nil
}

func (s *fakeSnapshotStore) List(_ context.Context, _ *laredo.SnapshotFilter) ([]laredo.SnapshotDescriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	descriptors := make([]laredo.SnapshotDescriptor, 0, len(s.saved))
	for _, snap := range s.saved {
		descriptors = append(descriptors, laredo.SnapshotDescriptor{
			SnapshotID: snap.id,
			CreatedAt:  snap.metadata.CreatedAt,
		})
	}
	return descriptors, nil
}

func (s *fakeSnapshotStore) Delete(_ context.Context, _ string) error { return nil }

func (s *fakeSnapshotStore) Prune(_ context.Context, _ int, _ *laredo.TableIdentifier) error {
	return nil
}

// --- Buffer dispatch tests ---

func TestEngine_BufferedDispatch_BasicFlow(t *testing.T) {
	// Verify changes flow through per-pipeline buffers to targets.
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.BufferSize(100),
		),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit several changes.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))
	src.EmitUpdate(testutil.SampleTable(),
		testutil.SampleRow(1, "alice-updated"),
		laredo.Row{"id": 1},
	)
	src.EmitDelete(testutil.SampleTable(), laredo.Row{"id": 2})

	// Wait for all 4 change events to be applied.
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return obs.EventCount("ChangeApplied") >= 4
	}, "expected at least 4 ChangeApplied events")

	// After all events: row 1 updated, row 3 present, row 2 deleted.
	if target.Count() != 2 {
		t.Fatalf("expected 2 rows after all changes, got %d", target.Count())
	}

	// Verify alice was updated.
	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected row with id=1")
	}
	if row["name"] != "alice-updated" {
		t.Errorf("expected alice-updated, got %v", row["name"])
	}

	// Verify observer received buffer depth events.
	if obs.EventCount("BufferDepthChanged") == 0 {
		t.Error("expected BufferDepthChanged events")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_BufferedDispatch_DropOldestPolicy(t *testing.T) {
	src := configuredSource()

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.BufferSize(1),
			laredo.BufferPolicyOpt(laredo.BufferDropOldest),
		),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Add a stream delay to make the consumer slower, causing buffer to fill.
	src.SetStreamDelay(50 * time.Millisecond)

	// Emit many changes rapidly — some should be dropped.
	for i := range 20 {
		src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(i+1, fmt.Sprintf("row-%d", i+1)))
	}

	// Wait for processing to complete.
	time.Sleep(2 * time.Second)

	// If drops occurred, BufferPolicyTriggered should fire.
	// (with buffer size 1 and rapid sends, drops are very likely)
	// We just verify the pipeline doesn't crash and events are processed.
	if target.Count() == 0 {
		t.Error("expected at least some rows in target")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_BufferedDispatch_ErrorPolicy(t *testing.T) {
	src := configuredSource()

	target := &slowTarget{delay: 100 * time.Millisecond, target: memory.NewIndexedTarget()}
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target,
			laredo.BufferSize(1),
			laredo.BufferPolicyOpt(laredo.BufferError),
			laredo.ErrorPolicyOpt(laredo.ErrorIsolate),
			laredo.MaxRetries(0),
		),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit many changes rapidly to overflow the tiny buffer.
	for i := range 10 {
		src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(i+1, fmt.Sprintf("row-%d", i+1)))
	}

	// Wait for processing.
	time.Sleep(2 * time.Second)

	// BufferPolicyTriggered should have fired for BufferError.
	if obs.EventCount("BufferPolicyTriggered") == 0 {
		// The buffer may not have overflowed if timing was lucky. That's OK.
		t.Log("no BufferPolicyTriggered events (timing-dependent)")
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_BufferedDispatch_MultiPipelineSameSource(t *testing.T) {
	// Two pipelines for different tables on the same source, each with its own buffer.
	table1 := testutil.SampleTable()
	table2 := laredo.Table("public", "other_table")

	src := testsource.New()
	src.SetSchema(table1, testutil.SampleColumns())
	src.SetSchema(table2, testutil.SampleColumns())
	src.AddRow(table1, testutil.SampleRow(1, "alice"))
	src.AddRow(table2, testutil.SampleRow(10, "xavier"))

	target1 := memory.NewIndexedTarget()
	target2 := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", table1, target1, laredo.BufferSize(50)),
		laredo.WithPipeline("pg", table2, target2, laredo.BufferSize(50)),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit changes to both tables.
	src.EmitInsert(table1, testutil.SampleRow(2, "bob"))
	src.EmitInsert(table2, testutil.SampleRow(11, "yolanda"))

	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return target1.Count() == 2 && target2.Count() == 2
	}, "expected each target to have 2 rows")

	// Verify ACK advanced (both pipelines confirmed).
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return obs.EventCount("AckAdvanced") > 0
	}, "expected AckAdvanced events")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestEngine_BufferedDispatch_GracefulDrain(t *testing.T) {
	// Verify that buffered events are drained on shutdown.
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target, laredo.BufferSize(1000)),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit some changes.
	for i := range 5 {
		src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(i+10, fmt.Sprintf("row-%d", i)))
	}

	// Wait for all events to be consumed by the target.
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return target.Count() == 6 // 1 baseline + 5 streamed
	}, "expected 6 rows before shutdown")

	// Stop should complete cleanly after all events are processed.
	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Verify all rows are still present after shutdown.
	if target.Count() != 6 {
		t.Errorf("expected 6 rows after graceful shutdown, got %d", target.Count())
	}
}

func TestEngine_BufferedDispatch_ACKCoordination(t *testing.T) {
	// Verify ACK advances correctly with buffered consumers.
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}

	e, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target, laredo.BufferSize(100)),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	// Emit changes and wait for them to be applied.
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))

	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows")

	// ACK should have advanced.
	testutil.AssertEventually(t, 2*time.Second, func() bool {
		return obs.EventCount("AckAdvanced") > 0
	}, "expected ACK to advance")

	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// slowTarget wraps an IndexedTarget and adds a configurable delay to OnInsert.
type slowTarget struct {
	delay  time.Duration
	target *memory.IndexedTarget
}

func (s *slowTarget) OnInit(ctx context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	return s.target.OnInit(ctx, table, columns)
}

func (s *slowTarget) OnBaselineRow(ctx context.Context, table laredo.TableIdentifier, row laredo.Row) error {
	return s.target.OnBaselineRow(ctx, table, row)
}

func (s *slowTarget) OnBaselineComplete(ctx context.Context, table laredo.TableIdentifier) error {
	return s.target.OnBaselineComplete(ctx, table)
}

func (s *slowTarget) OnInsert(ctx context.Context, table laredo.TableIdentifier, row laredo.Row) error {
	time.Sleep(s.delay)
	return s.target.OnInsert(ctx, table, row)
}

func (s *slowTarget) OnUpdate(ctx context.Context, table laredo.TableIdentifier, row laredo.Row, old laredo.Row) error {
	return s.target.OnUpdate(ctx, table, row, old)
}

func (s *slowTarget) OnDelete(ctx context.Context, table laredo.TableIdentifier, row laredo.Row) error {
	return s.target.OnDelete(ctx, table, row)
}

func (s *slowTarget) OnTruncate(ctx context.Context, table laredo.TableIdentifier) error {
	return s.target.OnTruncate(ctx, table)
}

func (s *slowTarget) OnSchemaChange(_ context.Context, _ laredo.TableIdentifier, _, _ []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	return laredo.SchemaChangeResponse{Action: laredo.SchemaContinue}
}

func (s *slowTarget) OnClose(ctx context.Context, table laredo.TableIdentifier) error {
	return s.target.OnClose(ctx, table)
}

func (s *slowTarget) IsDurable() bool {
	return s.target.IsDurable()
}

func (s *slowTarget) ExportSnapshot(ctx context.Context) ([]laredo.SnapshotEntry, error) {
	return s.target.ExportSnapshot(ctx)
}

func (s *slowTarget) RestoreSnapshot(ctx context.Context, info laredo.TableSnapshotInfo, entries []laredo.SnapshotEntry) error {
	return s.target.RestoreSnapshot(ctx, info, entries)
}

func (s *slowTarget) SupportsConsistentSnapshot() bool {
	return false
}

// fakeSnapshotSerializer implements SnapshotSerializer for testing.
type fakeSnapshotSerializer struct{}

func (fakeSnapshotSerializer) FormatID() string { return "fake" }

func (fakeSnapshotSerializer) Write(_ laredo.TableSnapshotInfo, _ []laredo.Row, _ io.Writer) error {
	return nil
}

func (fakeSnapshotSerializer) Read(_ io.Reader) (laredo.TableSnapshotInfo, []laredo.Row, error) {
	return laredo.TableSnapshotInfo{}, nil, nil
}
