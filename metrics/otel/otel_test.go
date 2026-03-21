package otel_test

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/zourzouvillys/laredo"
	laredootel "github.com/zourzouvillys/laredo/metrics/otel"
)

func newTestObserver(t *testing.T) (*laredootel.Observer, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	obs, err := laredootel.New(provider)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return obs, reader
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) *metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return &rm
}

func findMetric(rm *metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func TestObserver_ImplementsInterface(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	obs, err := laredootel.New(provider)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var _ laredo.EngineObserver = obs
}

func TestObserver_ChangesApplied(t *testing.T) {
	obs, reader := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnChangeReceived("pipe-1", table, laredo.ActionInsert, nil)
	obs.OnChangeReceived("pipe-1", table, laredo.ActionInsert, nil)
	obs.OnChangeApplied("pipe-1", table, laredo.ActionInsert, 5*time.Millisecond)

	rm := collectMetrics(t, reader)

	m := findMetric(rm, "laredo.changes.received")
	if m == nil {
		t.Fatal("expected laredo.changes.received metric")
	}

	m = findMetric(rm, "laredo.changes.applied")
	if m == nil {
		t.Fatal("expected laredo.changes.applied metric")
	}

	m = findMetric(rm, "laredo.change.apply.duration")
	if m == nil {
		t.Fatal("expected laredo.change.apply.duration metric")
	}
}

func TestObserver_Errors(t *testing.T) {
	obs, reader := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnChangeError("pipe-1", table, laredo.ActionInsert, laredo.ErrorInfo{Message: "fail"})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "laredo.change.errors")
	if m == nil {
		t.Fatal("expected laredo.change.errors metric")
	}
}

func TestObserver_BufferDepth(t *testing.T) {
	obs, reader := newTestObserver(t)

	obs.OnBufferDepthChanged("pipe-1", 42, 100)

	rm := collectMetrics(t, reader)
	if findMetric(rm, "laredo.pipeline.buffer.depth") == nil {
		t.Error("expected laredo.pipeline.buffer.depth metric")
	}
	if findMetric(rm, "laredo.pipeline.buffer.capacity") == nil {
		t.Error("expected laredo.pipeline.buffer.capacity metric")
	}
}

func TestObserver_Lag(t *testing.T) {
	obs, reader := newTestObserver(t)

	lag := 5 * time.Second
	obs.OnLagUpdated("pg-main", 1024, &lag)

	rm := collectMetrics(t, reader)
	if findMetric(rm, "laredo.source.lag.bytes") == nil {
		t.Error("expected laredo.source.lag.bytes metric")
	}
	if findMetric(rm, "laredo.source.lag.milliseconds") == nil {
		t.Error("expected laredo.source.lag.milliseconds metric")
	}
}

func TestObserver_Snapshots(t *testing.T) {
	obs, reader := newTestObserver(t)

	obs.OnSnapshotCompleted("snap-1", 2, 100, 4096, 3*time.Second)
	obs.OnSnapshotFailed("snap-2", laredo.ErrorInfo{Message: "disk full"})

	rm := collectMetrics(t, reader)
	if findMetric(rm, "laredo.snapshots") == nil {
		t.Error("expected laredo.snapshots metric")
	}
	if findMetric(rm, "laredo.snapshot.duration") == nil {
		t.Error("expected laredo.snapshot.duration metric")
	}
}

func TestObserver_DeadLetters(t *testing.T) {
	obs, reader := newTestObserver(t)

	obs.OnDeadLetterWritten("pipe-1", laredo.ChangeEvent{}, laredo.ErrorInfo{})

	rm := collectMetrics(t, reader)
	if findMetric(rm, "laredo.dead_letters") == nil {
		t.Error("expected laredo.dead_letters metric")
	}
}

func TestObserver_Validation(t *testing.T) {
	obs, reader := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnValidationResult("pipe-1", table, 100, 100, true)
	obs.OnValidationResult("pipe-1", table, 100, 99, false)

	rm := collectMetrics(t, reader)
	if findMetric(rm, "laredo.validation.runs") == nil {
		t.Error("expected laredo.validation.runs metric")
	}
	if findMetric(rm, "laredo.validation.target.row_count") == nil {
		t.Error("expected laredo.validation.target.row_count metric")
	}
}

func TestObserver_Baseline(t *testing.T) {
	obs, reader := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnBaselineCompleted("pipe-1", table, 500, 2*time.Second)

	rm := collectMetrics(t, reader)
	if findMetric(rm, "laredo.baseline.duration") == nil {
		t.Error("expected laredo.baseline.duration metric")
	}
	if findMetric(rm, "laredo.baseline.rows") == nil {
		t.Error("expected laredo.baseline.rows metric")
	}
}

func TestObserver_PipelineState(t *testing.T) {
	obs, reader := newTestObserver(t)

	obs.OnPipelineStateChanged("pipe-1", laredo.PipelineInitializing, laredo.PipelineStreaming)

	rm := collectMetrics(t, reader)
	if findMetric(rm, "laredo.pipeline.state") == nil {
		t.Error("expected laredo.pipeline.state metric")
	}
}
