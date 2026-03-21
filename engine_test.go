package laredo_test

import (
	"context"
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
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

func TestEngine_ReadinessStubs(t *testing.T) {
	e, errs := laredo.NewEngine(validOpts()...)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Readiness stubs return false until implemented.
	if e.IsReady() {
		t.Error("expected IsReady to return false (stub)")
	}
	if e.AwaitReady(0) {
		t.Error("expected AwaitReady to return false (stub)")
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
