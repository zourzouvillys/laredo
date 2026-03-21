// Package prometheus implements an EngineObserver that exports metrics to Prometheus.
package prometheus

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/zourzouvillys/laredo"
)

// Observer implements laredo.EngineObserver for Prometheus metrics.
type Observer struct {
	reg *prometheus.Registry

	// Gauges
	pipelineState    *prometheus.GaugeVec
	bufferDepth      *prometheus.GaugeVec
	bufferCapacity   *prometheus.GaugeVec
	sourceLagBytes   *prometheus.GaugeVec
	sourceLagSeconds *prometheus.GaugeVec

	// Counters
	changesReceived *prometheus.CounterVec
	changesApplied  *prometheus.CounterVec
	changeErrors    *prometheus.CounterVec
	deadLetters     *prometheus.CounterVec
	rowsExpired     *prometheus.CounterVec
	snapshotsTotal  *prometheus.CounterVec
	validationRuns  *prometheus.CounterVec

	// Histograms
	changeApplyDuration *prometheus.HistogramVec
	baselineDuration    *prometheus.HistogramVec
	snapshotDuration    prometheus.Histogram
	baselineRowsLoaded  *prometheus.CounterVec
	baselineRowsTotal   *prometheus.GaugeVec
	validationRowCount  *prometheus.GaugeVec
}

var _ laredo.EngineObserver = (*Observer)(nil)

// New creates a new Prometheus observer. If reg is nil, the default
// prometheus registry is used.
func New(reg *prometheus.Registry) *Observer {
	if reg == nil {
		reg = prometheus.DefaultRegisterer.(*prometheus.Registry)
	}

	o := &Observer{
		reg: reg,

		pipelineState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "laredo_pipeline_state",
			Help: "Current pipeline state (0=init, 1=baseline, 2=streaming, 3=paused, 4=error, 5=stopped).",
		}, []string{"pipeline"}),

		bufferDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "laredo_pipeline_buffer_depth",
			Help: "Current number of items in the pipeline change buffer.",
		}, []string{"pipeline"}),

		bufferCapacity: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "laredo_pipeline_buffer_capacity",
			Help: "Maximum capacity of the pipeline change buffer.",
		}, []string{"pipeline"}),

		sourceLagBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "laredo_source_lag_bytes",
			Help: "Replication lag in bytes for a source.",
		}, []string{"source"}),

		sourceLagSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "laredo_source_lag_seconds",
			Help: "Replication lag in seconds for a source.",
		}, []string{"source"}),

		changesReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "laredo_changes_received_total",
			Help: "Total number of change events received per pipeline and action.",
		}, []string{"pipeline", "action"}),

		changesApplied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "laredo_changes_applied_total",
			Help: "Total number of change events successfully applied per pipeline and action.",
		}, []string{"pipeline", "action"}),

		changeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "laredo_change_errors_total",
			Help: "Total number of change application errors per pipeline.",
		}, []string{"pipeline"}),

		deadLetters: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "laredo_dead_letters_total",
			Help: "Total number of changes written to dead letter store per pipeline.",
		}, []string{"pipeline"}),

		rowsExpired: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "laredo_rows_expired_total",
			Help: "Total number of rows removed by TTL expiry per pipeline.",
		}, []string{"pipeline"}),

		snapshotsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "laredo_snapshots_total",
			Help: "Total number of snapshots by outcome (created, failed).",
		}, []string{"outcome"}),

		validationRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "laredo_validation_runs_total",
			Help: "Total number of validation checks per pipeline and result.",
		}, []string{"pipeline", "result"}),

		changeApplyDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "laredo_change_apply_duration_seconds",
			Help:    "Time to apply a single change event.",
			Buckets: prometheus.DefBuckets,
		}, []string{"pipeline", "action"}),

		baselineDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "laredo_baseline_duration_seconds",
			Help:    "Time to complete a full baseline load.",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300},
		}, []string{"pipeline"}),

		snapshotDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "laredo_snapshot_duration_seconds",
			Help:    "Time to create a snapshot.",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120},
		}),

		baselineRowsLoaded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "laredo_baseline_rows_loaded_total",
			Help: "Total number of baseline rows loaded per pipeline.",
		}, []string{"pipeline"}),

		baselineRowsTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "laredo_baseline_rows_total",
			Help: "Total rows loaded in the last completed baseline per pipeline.",
		}, []string{"pipeline"}),

		validationRowCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "laredo_validation_target_row_count",
			Help: "Target row count from last validation check per pipeline.",
		}, []string{"pipeline"}),
	}

	reg.MustRegister(
		o.pipelineState,
		o.bufferDepth,
		o.bufferCapacity,
		o.sourceLagBytes,
		o.sourceLagSeconds,
		o.changesReceived,
		o.changesApplied,
		o.changeErrors,
		o.deadLetters,
		o.rowsExpired,
		o.snapshotsTotal,
		o.validationRuns,
		o.changeApplyDuration,
		o.baselineDuration,
		o.snapshotDuration,
		o.baselineRowsLoaded,
		o.baselineRowsTotal,
		o.validationRowCount,
	)

	return o
}

// Handler returns an HTTP handler for the /metrics endpoint.
func (o *Observer) Handler() http.Handler {
	return promhttp.HandlerFor(o.reg, promhttp.HandlerOpts{})
}

// --- Lifecycle ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSourceConnected(string, string) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSourceDisconnected(string, string) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnPipelineStateChanged(pipelineID string, _ laredo.PipelineState, newState laredo.PipelineState) {
	o.pipelineState.WithLabelValues(pipelineID).Set(float64(newState))
}

// --- Baseline ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBaselineStarted(string, laredo.TableIdentifier) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBaselineRowLoaded(pipelineID string, _ laredo.TableIdentifier, _ int64) {
	o.baselineRowsLoaded.WithLabelValues(pipelineID).Inc()
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBaselineCompleted(pipelineID string, _ laredo.TableIdentifier, totalRows int64, duration time.Duration) {
	o.baselineDuration.WithLabelValues(pipelineID).Observe(duration.Seconds())
	o.baselineRowsTotal.WithLabelValues(pipelineID).Set(float64(totalRows))
}

// --- Streaming ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnChangeReceived(pipelineID string, _ laredo.TableIdentifier, action laredo.ChangeAction, _ laredo.Position) {
	o.changesReceived.WithLabelValues(pipelineID, action.String()).Inc()
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnChangeApplied(pipelineID string, _ laredo.TableIdentifier, action laredo.ChangeAction, duration time.Duration) {
	o.changesApplied.WithLabelValues(pipelineID, action.String()).Inc()
	o.changeApplyDuration.WithLabelValues(pipelineID, action.String()).Observe(duration.Seconds())
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnChangeError(pipelineID string, _ laredo.TableIdentifier, _ laredo.ChangeAction, _ laredo.ErrorInfo) {
	o.changeErrors.WithLabelValues(pipelineID).Inc()
}

// --- ACK ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnAckAdvanced(string, laredo.Position) {}

// --- Backpressure ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBufferDepthChanged(pipelineID string, depth, capacity int) {
	o.bufferDepth.WithLabelValues(pipelineID).Set(float64(depth))
	o.bufferCapacity.WithLabelValues(pipelineID).Set(float64(capacity))
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnBufferPolicyTriggered(string, laredo.BufferPolicy) {}

// --- Snapshots ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSnapshotStarted(string) {}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSnapshotCompleted(_ string, _ int, _ int64, _ int64, duration time.Duration) {
	o.snapshotsTotal.WithLabelValues("created").Inc()
	o.snapshotDuration.Observe(duration.Seconds())
}

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnSnapshotFailed(string, laredo.ErrorInfo) {
	o.snapshotsTotal.WithLabelValues("failed").Inc()
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
	o.sourceLagBytes.WithLabelValues(sourceID).Set(float64(lagBytes))
	if lagTime != nil {
		o.sourceLagSeconds.WithLabelValues(sourceID).Set(lagTime.Seconds())
	}
}

// --- Dead letters ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnDeadLetterWritten(pipelineID string, _ laredo.ChangeEvent, _ laredo.ErrorInfo) {
	o.deadLetters.WithLabelValues(pipelineID).Inc()
}

// --- TTL ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnRowExpired(pipelineID string, _ laredo.TableIdentifier, _ string) {
	o.rowsExpired.WithLabelValues(pipelineID).Inc()
}

// --- Validation ---

//nolint:revive // EngineObserver implementation.
func (o *Observer) OnValidationResult(pipelineID string, _ laredo.TableIdentifier, _ int64, targetCount int64, match bool) {
	result := "match"
	if !match {
		result = "mismatch"
	}
	o.validationRuns.WithLabelValues(pipelineID, result).Inc()
	o.validationRowCount.WithLabelValues(pipelineID).Set(float64(targetCount))
}

// --- Fan-out (no-op for now, populated when fan-out target is implemented) ---

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
