package laredo

import "time"

// ErrorInfo describes an error for observer callbacks.
type ErrorInfo struct {
	Err     error
	Message string
}

// SchemaChangeEvent describes a schema change for observer callbacks.
type SchemaChangeEvent struct {
	OldColumns []ColumnDefinition
	NewColumns []ColumnDefinition
}

// SyncMode describes the mode a fan-out client connected with.
type SyncMode int

// Fan-out client sync modes.
const (
	SyncModeFullSnapshot      SyncMode = iota // Server sends full snapshot.
	SyncModeDelta                             // Server sends journal delta.
	SyncModeDeltaFromSnapshot                 // Client uses local snapshot, server sends delta.
)

// EngineObserver receives structured lifecycle and operational events from the engine.
// All methods are called synchronously from the engine goroutine. Implementations
// must not block.
type EngineObserver interface {
	// Lifecycle
	OnSourceConnected(sourceID, sourceType string)
	OnSourceDisconnected(sourceID, reason string)
	OnPipelineStateChanged(pipelineID string, oldState, newState PipelineState)

	// Baseline
	OnBaselineStarted(pipelineID string, table TableIdentifier)
	OnBaselineRowLoaded(pipelineID string, table TableIdentifier, rowCount int64)
	OnBaselineCompleted(pipelineID string, table TableIdentifier, totalRows int64, duration time.Duration)

	// Streaming
	OnChangeReceived(pipelineID string, table TableIdentifier, action ChangeAction, position Position)
	OnChangeApplied(pipelineID string, table TableIdentifier, action ChangeAction, duration time.Duration)
	OnChangeError(pipelineID string, table TableIdentifier, action ChangeAction, err ErrorInfo)

	// ACK
	OnAckAdvanced(sourceID string, position Position)

	// Backpressure
	OnBufferDepthChanged(pipelineID string, depth, capacity int)
	OnBufferPolicyTriggered(pipelineID string, policy BufferPolicy)

	// Snapshots
	OnSnapshotStarted(snapshotID string)
	OnSnapshotCompleted(snapshotID string, tables int, rows, sizeBytes int64, duration time.Duration)
	OnSnapshotFailed(snapshotID string, err ErrorInfo)
	OnSnapshotRestoreStarted(snapshotID string)
	OnSnapshotRestoreCompleted(snapshotID string, duration time.Duration)

	// Schema
	OnSchemaChange(sourceID string, table TableIdentifier, change SchemaChangeEvent)

	// Lag
	OnLagUpdated(sourceID string, lagBytes int64, lagTime *time.Duration)

	// Dead letters
	OnDeadLetterWritten(pipelineID string, change ChangeEvent, err ErrorInfo)

	// TTL
	OnRowExpired(pipelineID string, table TableIdentifier, key string)

	// Validation
	OnValidationResult(pipelineID string, table TableIdentifier, sourceCount, targetCount int64, match bool)

	// Replication fan-out
	OnFanOutClientConnected(pipelineID, clientID string, syncMode SyncMode)
	OnFanOutClientDisconnected(pipelineID, clientID, reason string)
	OnFanOutClientCaughtUp(pipelineID, clientID string, sequence int64)
	OnFanOutClientBackpressure(pipelineID, clientID string, bufferDepth int)
	OnFanOutSnapshotCreated(pipelineID, snapshotID string, sequence, rows int64)
	OnFanOutJournalPruned(pipelineID string, entriesPruned, oldestSequence int64)
}

// NullObserver is a no-op EngineObserver for embedded users who don't need observability.
// All methods are intentionally empty — see EngineObserver for method documentation.
type NullObserver struct{}

var _ EngineObserver = NullObserver{}

//nolint:revive // EngineObserver no-op implementation; docs are on the interface.
func (NullObserver) OnSourceConnected(string, string) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnSourceDisconnected(string, string) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnPipelineStateChanged(string, PipelineState, PipelineState) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnBaselineStarted(string, TableIdentifier) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnBaselineRowLoaded(string, TableIdentifier, int64) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnBaselineCompleted(string, TableIdentifier, int64, time.Duration) {
}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnChangeReceived(string, TableIdentifier, ChangeAction, Position) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnChangeApplied(string, TableIdentifier, ChangeAction, time.Duration) {
}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnChangeError(string, TableIdentifier, ChangeAction, ErrorInfo) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnAckAdvanced(string, Position) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnBufferDepthChanged(string, int, int) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnBufferPolicyTriggered(string, BufferPolicy) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnSnapshotStarted(string) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnSnapshotCompleted(string, int, int64, int64, time.Duration) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnSnapshotFailed(string, ErrorInfo) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnSnapshotRestoreStarted(string) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnSnapshotRestoreCompleted(string, time.Duration) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnSchemaChange(string, TableIdentifier, SchemaChangeEvent) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnLagUpdated(string, int64, *time.Duration) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnDeadLetterWritten(string, ChangeEvent, ErrorInfo) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnRowExpired(string, TableIdentifier, string) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnValidationResult(string, TableIdentifier, int64, int64, bool) {
}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnFanOutClientConnected(string, string, SyncMode) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnFanOutClientDisconnected(string, string, string) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnFanOutClientCaughtUp(string, string, int64) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnFanOutClientBackpressure(string, string, int) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnFanOutSnapshotCreated(string, string, int64, int64) {}

//nolint:revive // EngineObserver no-op implementation.
func (NullObserver) OnFanOutJournalPruned(string, int64, int64) {}

// CompositeObserver fans out observer calls to multiple EngineObserver implementations.
// All methods delegate to each registered observer in order — see EngineObserver for
// method documentation.
type CompositeObserver struct {
	observers []EngineObserver
}

var _ EngineObserver = (*CompositeObserver)(nil)

// NewCompositeObserver creates a CompositeObserver. Nil observers are filtered out.
func NewCompositeObserver(observers ...EngineObserver) *CompositeObserver {
	var filtered []EngineObserver
	for _, o := range observers {
		if o != nil {
			filtered = append(filtered, o)
		}
	}
	return &CompositeObserver{observers: filtered}
}

//nolint:revive // EngineObserver fan-out implementation; docs are on the interface.
func (c *CompositeObserver) OnSourceConnected(sourceID, sourceType string) {
	for _, o := range c.observers {
		o.OnSourceConnected(sourceID, sourceType)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnSourceDisconnected(sourceID, reason string) {
	for _, o := range c.observers {
		o.OnSourceDisconnected(sourceID, reason)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnPipelineStateChanged(pipelineID string, oldState, newState PipelineState) {
	for _, o := range c.observers {
		o.OnPipelineStateChanged(pipelineID, oldState, newState)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnBaselineStarted(pipelineID string, table TableIdentifier) {
	for _, o := range c.observers {
		o.OnBaselineStarted(pipelineID, table)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnBaselineRowLoaded(pipelineID string, table TableIdentifier, rowCount int64) {
	for _, o := range c.observers {
		o.OnBaselineRowLoaded(pipelineID, table, rowCount)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnBaselineCompleted(pipelineID string, table TableIdentifier, totalRows int64, duration time.Duration) {
	for _, o := range c.observers {
		o.OnBaselineCompleted(pipelineID, table, totalRows, duration)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnChangeReceived(pipelineID string, table TableIdentifier, action ChangeAction, position Position) {
	for _, o := range c.observers {
		o.OnChangeReceived(pipelineID, table, action, position)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnChangeApplied(pipelineID string, table TableIdentifier, action ChangeAction, duration time.Duration) {
	for _, o := range c.observers {
		o.OnChangeApplied(pipelineID, table, action, duration)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnChangeError(pipelineID string, table TableIdentifier, action ChangeAction, err ErrorInfo) {
	for _, o := range c.observers {
		o.OnChangeError(pipelineID, table, action, err)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnAckAdvanced(sourceID string, position Position) {
	for _, o := range c.observers {
		o.OnAckAdvanced(sourceID, position)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnBufferDepthChanged(pipelineID string, depth, capacity int) {
	for _, o := range c.observers {
		o.OnBufferDepthChanged(pipelineID, depth, capacity)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnBufferPolicyTriggered(pipelineID string, policy BufferPolicy) {
	for _, o := range c.observers {
		o.OnBufferPolicyTriggered(pipelineID, policy)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnSnapshotStarted(snapshotID string) {
	for _, o := range c.observers {
		o.OnSnapshotStarted(snapshotID)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnSnapshotCompleted(snapshotID string, tables int, rows, sizeBytes int64, duration time.Duration) {
	for _, o := range c.observers {
		o.OnSnapshotCompleted(snapshotID, tables, rows, sizeBytes, duration)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnSnapshotFailed(snapshotID string, err ErrorInfo) {
	for _, o := range c.observers {
		o.OnSnapshotFailed(snapshotID, err)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnSnapshotRestoreStarted(snapshotID string) {
	for _, o := range c.observers {
		o.OnSnapshotRestoreStarted(snapshotID)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnSnapshotRestoreCompleted(snapshotID string, duration time.Duration) {
	for _, o := range c.observers {
		o.OnSnapshotRestoreCompleted(snapshotID, duration)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnSchemaChange(sourceID string, table TableIdentifier, change SchemaChangeEvent) {
	for _, o := range c.observers {
		o.OnSchemaChange(sourceID, table, change)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnLagUpdated(sourceID string, lagBytes int64, lagTime *time.Duration) {
	for _, o := range c.observers {
		o.OnLagUpdated(sourceID, lagBytes, lagTime)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnDeadLetterWritten(pipelineID string, change ChangeEvent, err ErrorInfo) {
	for _, o := range c.observers {
		o.OnDeadLetterWritten(pipelineID, change, err)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnRowExpired(pipelineID string, table TableIdentifier, key string) {
	for _, o := range c.observers {
		o.OnRowExpired(pipelineID, table, key)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnValidationResult(pipelineID string, table TableIdentifier, sourceCount, targetCount int64, match bool) {
	for _, o := range c.observers {
		o.OnValidationResult(pipelineID, table, sourceCount, targetCount, match)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnFanOutClientConnected(pipelineID, clientID string, syncMode SyncMode) {
	for _, o := range c.observers {
		o.OnFanOutClientConnected(pipelineID, clientID, syncMode)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnFanOutClientDisconnected(pipelineID, clientID, reason string) {
	for _, o := range c.observers {
		o.OnFanOutClientDisconnected(pipelineID, clientID, reason)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnFanOutClientCaughtUp(pipelineID, clientID string, sequence int64) {
	for _, o := range c.observers {
		o.OnFanOutClientCaughtUp(pipelineID, clientID, sequence)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnFanOutClientBackpressure(pipelineID, clientID string, bufferDepth int) {
	for _, o := range c.observers {
		o.OnFanOutClientBackpressure(pipelineID, clientID, bufferDepth)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnFanOutSnapshotCreated(pipelineID, snapshotID string, sequence, rows int64) {
	for _, o := range c.observers {
		o.OnFanOutSnapshotCreated(pipelineID, snapshotID, sequence, rows)
	}
}

//nolint:revive // EngineObserver fan-out implementation.
func (c *CompositeObserver) OnFanOutJournalPruned(pipelineID string, entriesPruned, oldestSequence int64) {
	for _, o := range c.observers {
		o.OnFanOutJournalPruned(pipelineID, entriesPruned, oldestSequence)
	}
}
