// Package otel implements an EngineObserver that exports metrics via OpenTelemetry.
package otel

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/zourzouvillys/laredo"
)

// Observer implements laredo.EngineObserver for OpenTelemetry metrics.
// All metric names use the "laredo." prefix for consistency with the
// Prometheus observer.
type Observer struct {
	// Gauges (UpDownCounters used as gauges via Set-like pattern)
	pipelineState  metric.Int64Gauge
	bufferDepth    metric.Int64Gauge
	bufferCapacity metric.Int64Gauge
	sourceLagBytes metric.Int64Gauge
	sourceLagMs    metric.Int64Gauge

	// Counters
	changesReceived metric.Int64Counter
	changesApplied  metric.Int64Counter
	changeErrors    metric.Int64Counter
	deadLetters     metric.Int64Counter
	rowsExpired     metric.Int64Counter
	snapshotsTotal  metric.Int64Counter
	validationRuns  metric.Int64Counter
	baselineRows    metric.Int64Counter

	// Histograms
	changeApplyDuration metric.Float64Histogram
	baselineDuration    metric.Float64Histogram
	snapshotDuration    metric.Float64Histogram

	// Gauges for last-known values
	baselineRowsTotal  metric.Int64Gauge
	validationRowCount metric.Int64Gauge
}

var _ laredo.EngineObserver = (*Observer)(nil)

// New creates a new OpenTelemetry observer using the given MeterProvider.
// If provider is nil, the global MeterProvider is used.
func New(provider metric.MeterProvider) (*Observer, error) {
	meter := provider.Meter("laredo")

	o := &Observer{}
	var err error

	// Gauges
	if o.pipelineState, err = meter.Int64Gauge("laredo.pipeline.state",
		metric.WithDescription("Current pipeline state")); err != nil {
		return nil, fmt.Errorf("create pipeline_state: %w", err)
	}
	if o.bufferDepth, err = meter.Int64Gauge("laredo.pipeline.buffer.depth",
		metric.WithDescription("Current buffer depth")); err != nil {
		return nil, fmt.Errorf("create buffer_depth: %w", err)
	}
	if o.bufferCapacity, err = meter.Int64Gauge("laredo.pipeline.buffer.capacity",
		metric.WithDescription("Buffer capacity")); err != nil {
		return nil, fmt.Errorf("create buffer_capacity: %w", err)
	}
	if o.sourceLagBytes, err = meter.Int64Gauge("laredo.source.lag.bytes",
		metric.WithDescription("Source replication lag in bytes")); err != nil {
		return nil, fmt.Errorf("create source_lag_bytes: %w", err)
	}
	if o.sourceLagMs, err = meter.Int64Gauge("laredo.source.lag.milliseconds",
		metric.WithDescription("Source replication lag in milliseconds")); err != nil {
		return nil, fmt.Errorf("create source_lag_ms: %w", err)
	}
	if o.baselineRowsTotal, err = meter.Int64Gauge("laredo.baseline.rows",
		metric.WithDescription("Total rows from last baseline")); err != nil {
		return nil, fmt.Errorf("create baseline_rows_total: %w", err)
	}
	if o.validationRowCount, err = meter.Int64Gauge("laredo.validation.target.row_count",
		metric.WithDescription("Target row count from last validation")); err != nil {
		return nil, fmt.Errorf("create validation_row_count: %w", err)
	}

	// Counters
	if o.changesReceived, err = meter.Int64Counter("laredo.changes.received",
		metric.WithDescription("Changes received")); err != nil {
		return nil, fmt.Errorf("create changes_received: %w", err)
	}
	if o.changesApplied, err = meter.Int64Counter("laredo.changes.applied",
		metric.WithDescription("Changes applied")); err != nil {
		return nil, fmt.Errorf("create changes_applied: %w", err)
	}
	if o.changeErrors, err = meter.Int64Counter("laredo.change.errors",
		metric.WithDescription("Change errors")); err != nil {
		return nil, fmt.Errorf("create change_errors: %w", err)
	}
	if o.deadLetters, err = meter.Int64Counter("laredo.dead_letters",
		metric.WithDescription("Dead letters written")); err != nil {
		return nil, fmt.Errorf("create dead_letters: %w", err)
	}
	if o.rowsExpired, err = meter.Int64Counter("laredo.rows.expired",
		metric.WithDescription("Rows expired by TTL")); err != nil {
		return nil, fmt.Errorf("create rows_expired: %w", err)
	}
	if o.snapshotsTotal, err = meter.Int64Counter("laredo.snapshots",
		metric.WithDescription("Snapshots by outcome")); err != nil {
		return nil, fmt.Errorf("create snapshots: %w", err)
	}
	if o.validationRuns, err = meter.Int64Counter("laredo.validation.runs",
		metric.WithDescription("Validation runs")); err != nil {
		return nil, fmt.Errorf("create validation_runs: %w", err)
	}
	if o.baselineRows, err = meter.Int64Counter("laredo.baseline.rows.loaded",
		metric.WithDescription("Baseline rows loaded")); err != nil {
		return nil, fmt.Errorf("create baseline_rows_loaded: %w", err)
	}

	// Histograms
	if o.changeApplyDuration, err = meter.Float64Histogram("laredo.change.apply.duration",
		metric.WithDescription("Change apply duration in seconds"),
		metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("create change_apply_duration: %w", err)
	}
	if o.baselineDuration, err = meter.Float64Histogram("laredo.baseline.duration",
		metric.WithDescription("Baseline duration in seconds"),
		metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("create baseline_duration: %w", err)
	}
	if o.snapshotDuration, err = meter.Float64Histogram("laredo.snapshot.duration",
		metric.WithDescription("Snapshot duration in seconds"),
		metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("create snapshot_duration: %w", err)
	}

	return o, nil
}

var ctx = context.Background()

// --- Lifecycle ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSourceConnected(string, string) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSourceDisconnected(string, string) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnPipelineStateChanged(pipelineID string, _ laredo.PipelineState, newState laredo.PipelineState) {
	o.pipelineState.Record(ctx, int64(newState), metric.WithAttributes(pipelineAttr(pipelineID)))
}

// --- Baseline ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBaselineStarted(string, laredo.TableIdentifier) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBaselineRowLoaded(pipelineID string, _ laredo.TableIdentifier, _ int64) {
	o.baselineRows.Add(ctx, 1, metric.WithAttributes(pipelineAttr(pipelineID)))
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBaselineCompleted(pipelineID string, _ laredo.TableIdentifier, totalRows int64, duration time.Duration) {
	attrs := metric.WithAttributes(pipelineAttr(pipelineID))
	o.baselineDuration.Record(ctx, duration.Seconds(), attrs)
	o.baselineRowsTotal.Record(ctx, totalRows, attrs)
}

// --- Streaming ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnChangeReceived(pipelineID string, _ laredo.TableIdentifier, action laredo.ChangeAction, _ laredo.Position) {
	o.changesReceived.Add(ctx, 1, metric.WithAttributes(pipelineAttr(pipelineID), actionAttr(action)))
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnChangeApplied(pipelineID string, _ laredo.TableIdentifier, action laredo.ChangeAction, duration time.Duration) {
	attrs := metric.WithAttributes(pipelineAttr(pipelineID), actionAttr(action))
	o.changesApplied.Add(ctx, 1, attrs)
	o.changeApplyDuration.Record(ctx, duration.Seconds(), attrs)
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnChangeError(pipelineID string, _ laredo.TableIdentifier, _ laredo.ChangeAction, _ laredo.ErrorInfo) {
	o.changeErrors.Add(ctx, 1, metric.WithAttributes(pipelineAttr(pipelineID)))
}

// --- ACK ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnAckAdvanced(string, laredo.Position) {}

// --- Backpressure ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBufferDepthChanged(pipelineID string, depth, capacity int) {
	attrs := metric.WithAttributes(pipelineAttr(pipelineID))
	o.bufferDepth.Record(ctx, int64(depth), attrs)
	o.bufferCapacity.Record(ctx, int64(capacity), attrs)
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBufferPolicyTriggered(string, laredo.BufferPolicy) {}

// --- Snapshots ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSnapshotStarted(string) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSnapshotCompleted(_ string, _ int, _ int64, _ int64, duration time.Duration) {
	o.snapshotsTotal.Add(ctx, 1, metric.WithAttributes(outcomeAttr("created")))
	o.snapshotDuration.Record(ctx, duration.Seconds())
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSnapshotFailed(string, laredo.ErrorInfo) {
	o.snapshotsTotal.Add(ctx, 1, metric.WithAttributes(outcomeAttr("failed")))
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSnapshotRestoreStarted(string) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSnapshotRestoreCompleted(string, time.Duration) {}

// --- Schema ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSchemaChange(string, laredo.TableIdentifier, laredo.SchemaChangeEvent) {}

// --- Lag ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnLagUpdated(sourceID string, lagBytes int64, lagTime *time.Duration) {
	attrs := metric.WithAttributes(sourceAttr(sourceID))
	o.sourceLagBytes.Record(ctx, lagBytes, attrs)
	if lagTime != nil {
		o.sourceLagMs.Record(ctx, lagTime.Milliseconds(), attrs)
	}
}

// --- Dead letters ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnDeadLetterWritten(pipelineID string, _ laredo.ChangeEvent, _ laredo.ErrorInfo) {
	o.deadLetters.Add(ctx, 1, metric.WithAttributes(pipelineAttr(pipelineID)))
}

// --- TTL ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnRowExpired(pipelineID string, _ laredo.TableIdentifier, _ string) {
	o.rowsExpired.Add(ctx, 1, metric.WithAttributes(pipelineAttr(pipelineID)))
}

// --- Validation ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnValidationResult(pipelineID string, _ laredo.TableIdentifier, _ int64, targetCount int64, match bool) {
	result := "match"
	if !match {
		result = "mismatch"
	}
	attrs := metric.WithAttributes(pipelineAttr(pipelineID), resultAttr(result))
	o.validationRuns.Add(ctx, 1, attrs)
	o.validationRowCount.Record(ctx, targetCount, metric.WithAttributes(pipelineAttr(pipelineID)))
}

// --- Fan-out (no-op) ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnFanOutClientConnected(string, string, laredo.SyncMode) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnFanOutClientDisconnected(string, string, string) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnFanOutClientCaughtUp(string, string, int64) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnFanOutClientBackpressure(string, string, int) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnFanOutSnapshotCreated(string, string, int64, int64) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnFanOutJournalPruned(string, int64, int64) {}

// --- attribute helpers ---

func pipelineAttr(id string) attribute.KeyValue {
	return attribute.String("pipeline", id)
}

func actionAttr(a laredo.ChangeAction) attribute.KeyValue {
	return attribute.String("action", a.String())
}

func sourceAttr(id string) attribute.KeyValue {
	return attribute.String("source", id)
}

func outcomeAttr(outcome string) attribute.KeyValue {
	return attribute.String("outcome", outcome)
}

func resultAttr(result string) attribute.KeyValue {
	return attribute.String("result", result)
}
