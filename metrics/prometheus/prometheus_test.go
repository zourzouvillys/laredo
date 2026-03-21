package prometheus_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus "github.com/prometheus/client_model/go"

	"github.com/zourzouvillys/laredo"
	prom "github.com/zourzouvillys/laredo/metrics/prometheus"
)

func newTestObserver(t *testing.T) (*prom.Observer, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	obs := prom.New(reg)
	return obs, reg
}

func getMetric(t *testing.T, reg *prometheus.Registry, name string) *io_prometheus.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func TestObserver_ImplementsInterface(t *testing.T) {
	reg := prometheus.NewRegistry()
	var _ laredo.EngineObserver = prom.New(reg)
}

func TestObserver_PipelineState(t *testing.T) {
	obs, reg := newTestObserver(t)

	obs.OnPipelineStateChanged("pipe-1", laredo.PipelineInitializing, laredo.PipelineStreaming)

	fam := getMetric(t, reg, "laredo_pipeline_state")
	if fam == nil {
		t.Fatal("expected laredo_pipeline_state metric")
	}
	if len(fam.GetMetric()) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(fam.GetMetric()))
	}
	val := fam.GetMetric()[0].GetGauge().GetValue()
	if val != float64(laredo.PipelineStreaming) {
		t.Errorf("expected state=%d, got %f", laredo.PipelineStreaming, val)
	}
}

func TestObserver_ChangesApplied(t *testing.T) {
	obs, reg := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnChangeReceived("pipe-1", table, laredo.ActionInsert, nil)
	obs.OnChangeReceived("pipe-1", table, laredo.ActionInsert, nil)
	obs.OnChangeApplied("pipe-1", table, laredo.ActionInsert, 5*time.Millisecond)
	obs.OnChangeApplied("pipe-1", table, laredo.ActionInsert, 10*time.Millisecond)

	// Check received counter.
	fam := getMetric(t, reg, "laredo_changes_received_total")
	if fam == nil {
		t.Fatal("expected laredo_changes_received_total metric")
	}
	found := false
	for _, m := range fam.GetMetric() {
		if getLabel(m, "action") == laredo.ActionInsert.String() {
			if m.GetCounter().GetValue() != 2 {
				t.Errorf("expected received=2, got %f", m.GetCounter().GetValue())
			}
			found = true
		}
	}
	if !found {
		t.Error("expected metric with action=INSERT")
	}

	// Check applied counter.
	fam = getMetric(t, reg, "laredo_changes_applied_total")
	if fam == nil {
		t.Fatal("expected laredo_changes_applied_total metric")
	}
	for _, m := range fam.GetMetric() {
		if getLabel(m, "action") == laredo.ActionInsert.String() {
			if m.GetCounter().GetValue() != 2 {
				t.Errorf("expected applied=2, got %f", m.GetCounter().GetValue())
			}
		}
	}

	// Check histogram.
	fam = getMetric(t, reg, "laredo_change_apply_duration_seconds")
	if fam == nil {
		t.Fatal("expected laredo_change_apply_duration_seconds metric")
	}
	for _, m := range fam.GetMetric() {
		if getLabel(m, "action") == laredo.ActionInsert.String() {
			if m.GetHistogram().GetSampleCount() != 2 {
				t.Errorf("expected 2 observations, got %d", m.GetHistogram().GetSampleCount())
			}
		}
	}
}

func TestObserver_Errors(t *testing.T) {
	obs, reg := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnChangeError("pipe-1", table, laredo.ActionInsert, laredo.ErrorInfo{Message: "fail"})
	obs.OnChangeError("pipe-1", table, laredo.ActionUpdate, laredo.ErrorInfo{Message: "fail"})

	fam := getMetric(t, reg, "laredo_change_errors_total")
	if fam == nil {
		t.Fatal("expected laredo_change_errors_total metric")
	}
	if fam.GetMetric()[0].GetCounter().GetValue() != 2 {
		t.Errorf("expected errors=2, got %f", fam.GetMetric()[0].GetCounter().GetValue())
	}
}

func TestObserver_BufferDepth(t *testing.T) {
	obs, reg := newTestObserver(t)

	obs.OnBufferDepthChanged("pipe-1", 42, 100)

	fam := getMetric(t, reg, "laredo_pipeline_buffer_depth")
	if fam == nil {
		t.Fatal("expected laredo_pipeline_buffer_depth metric")
	}
	if fam.GetMetric()[0].GetGauge().GetValue() != 42 {
		t.Errorf("expected depth=42, got %f", fam.GetMetric()[0].GetGauge().GetValue())
	}

	fam = getMetric(t, reg, "laredo_pipeline_buffer_capacity")
	if fam == nil {
		t.Fatal("expected laredo_pipeline_buffer_capacity metric")
	}
	if fam.GetMetric()[0].GetGauge().GetValue() != 100 {
		t.Errorf("expected capacity=100, got %f", fam.GetMetric()[0].GetGauge().GetValue())
	}
}

func TestObserver_Lag(t *testing.T) {
	obs, reg := newTestObserver(t)

	lagTime := 5 * time.Second
	obs.OnLagUpdated("pg-main", 1024, &lagTime)

	fam := getMetric(t, reg, "laredo_source_lag_bytes")
	if fam == nil {
		t.Fatal("expected laredo_source_lag_bytes metric")
	}
	if fam.GetMetric()[0].GetGauge().GetValue() != 1024 {
		t.Errorf("expected lag_bytes=1024, got %f", fam.GetMetric()[0].GetGauge().GetValue())
	}

	fam = getMetric(t, reg, "laredo_source_lag_seconds")
	if fam == nil {
		t.Fatal("expected laredo_source_lag_seconds metric")
	}
	if fam.GetMetric()[0].GetGauge().GetValue() != 5.0 {
		t.Errorf("expected lag_seconds=5, got %f", fam.GetMetric()[0].GetGauge().GetValue())
	}
}

func TestObserver_Snapshots(t *testing.T) {
	obs, reg := newTestObserver(t)

	obs.OnSnapshotCompleted("snap-1", 2, 100, 4096, 3*time.Second)
	obs.OnSnapshotFailed("snap-2", laredo.ErrorInfo{Message: "disk full"})

	fam := getMetric(t, reg, "laredo_snapshots_total")
	if fam == nil {
		t.Fatal("expected laredo_snapshots_total metric")
	}
	for _, m := range fam.GetMetric() {
		switch getLabel(m, "outcome") {
		case "created":
			if m.GetCounter().GetValue() != 1 {
				t.Errorf("expected created=1, got %f", m.GetCounter().GetValue())
			}
		case "failed":
			if m.GetCounter().GetValue() != 1 {
				t.Errorf("expected failed=1, got %f", m.GetCounter().GetValue())
			}
		}
	}
}

func TestObserver_DeadLetters(t *testing.T) {
	obs, reg := newTestObserver(t)

	obs.OnDeadLetterWritten("pipe-1", laredo.ChangeEvent{}, laredo.ErrorInfo{})
	obs.OnDeadLetterWritten("pipe-1", laredo.ChangeEvent{}, laredo.ErrorInfo{})

	fam := getMetric(t, reg, "laredo_dead_letters_total")
	if fam == nil {
		t.Fatal("expected laredo_dead_letters_total metric")
	}
	if fam.GetMetric()[0].GetCounter().GetValue() != 2 {
		t.Errorf("expected dead_letters=2, got %f", fam.GetMetric()[0].GetCounter().GetValue())
	}
}

func TestObserver_RowsExpired(t *testing.T) {
	obs, reg := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnRowExpired("pipe-1", table, "row-1")

	fam := getMetric(t, reg, "laredo_rows_expired_total")
	if fam == nil {
		t.Fatal("expected laredo_rows_expired_total metric")
	}
	if fam.GetMetric()[0].GetCounter().GetValue() != 1 {
		t.Errorf("expected expired=1, got %f", fam.GetMetric()[0].GetCounter().GetValue())
	}
}

func TestObserver_Validation(t *testing.T) {
	obs, reg := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnValidationResult("pipe-1", table, 100, 100, true)
	obs.OnValidationResult("pipe-1", table, 100, 99, false)

	fam := getMetric(t, reg, "laredo_validation_runs_total")
	if fam == nil {
		t.Fatal("expected laredo_validation_runs_total metric")
	}
	for _, m := range fam.GetMetric() {
		switch getLabel(m, "result") {
		case "match":
			if m.GetCounter().GetValue() != 1 {
				t.Errorf("expected match=1, got %f", m.GetCounter().GetValue())
			}
		case "mismatch":
			if m.GetCounter().GetValue() != 1 {
				t.Errorf("expected mismatch=1, got %f", m.GetCounter().GetValue())
			}
		}
	}

	fam = getMetric(t, reg, "laredo_validation_target_row_count")
	if fam == nil {
		t.Fatal("expected laredo_validation_target_row_count metric")
	}
	// Last value should be 99.
	if fam.GetMetric()[0].GetGauge().GetValue() != 99 {
		t.Errorf("expected row_count=99, got %f", fam.GetMetric()[0].GetGauge().GetValue())
	}
}

func TestObserver_Baseline(t *testing.T) {
	obs, reg := newTestObserver(t)

	table := laredo.Table("public", "test")
	obs.OnBaselineCompleted("pipe-1", table, 500, 2*time.Second)

	fam := getMetric(t, reg, "laredo_baseline_duration_seconds")
	if fam == nil {
		t.Fatal("expected laredo_baseline_duration_seconds metric")
	}
	if fam.GetMetric()[0].GetHistogram().GetSampleCount() != 1 {
		t.Errorf("expected 1 observation, got %d", fam.GetMetric()[0].GetHistogram().GetSampleCount())
	}

	fam = getMetric(t, reg, "laredo_baseline_rows_total")
	if fam == nil {
		t.Fatal("expected laredo_baseline_rows_total metric")
	}
	if fam.GetMetric()[0].GetGauge().GetValue() != 500 {
		t.Errorf("expected rows=500, got %f", fam.GetMetric()[0].GetGauge().GetValue())
	}
}

func TestObserver_Handler(t *testing.T) {
	obs, _ := newTestObserver(t)
	handler := obs.Handler()
	if handler == nil {
		t.Fatal("expected non-nil HTTP handler")
	}
}

func getLabel(m *io_prometheus.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}
