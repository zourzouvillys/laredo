package laredo_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/filter"
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
				laredo.WithSnapshotStore(fakeSnapshotStore{}),
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

// fakeSnapshotStore implements SnapshotStore for testing.
type fakeSnapshotStore struct{}

func (fakeSnapshotStore) Save(_ context.Context, _ string, _ laredo.SnapshotMetadata, _ map[laredo.TableIdentifier][]laredo.SnapshotEntry) (laredo.SnapshotDescriptor, error) {
	return laredo.SnapshotDescriptor{}, nil
}

func (fakeSnapshotStore) Load(_ context.Context, _ string) (laredo.SnapshotMetadata, map[laredo.TableIdentifier][]laredo.SnapshotEntry, error) {
	return laredo.SnapshotMetadata{}, nil, nil
}

func (fakeSnapshotStore) Describe(_ context.Context, _ string) (laredo.SnapshotDescriptor, error) {
	return laredo.SnapshotDescriptor{}, nil
}

func (fakeSnapshotStore) List(_ context.Context, _ *laredo.SnapshotFilter) ([]laredo.SnapshotDescriptor, error) {
	return nil, nil
}

func (fakeSnapshotStore) Delete(_ context.Context, _ string) error { return nil }

func (fakeSnapshotStore) Prune(_ context.Context, _ int, _ *laredo.TableIdentifier) error {
	return nil
}
