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

const (
	SyncModeFullSnapshot     SyncMode = iota // Server sends full snapshot.
	SyncModeDelta                            // Server sends journal delta.
	SyncModeDeltaFromSnapshot                // Client uses local snapshot, server sends delta.
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
