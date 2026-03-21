// Package testutil provides test helpers for laredo.
package testutil

import (
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
)

// TestObserver is an EngineObserver that records all events for test assertions.
type TestObserver struct {
	mu     sync.Mutex
	Events []Event
}

// Event is a recorded observer event.
type Event struct {
	Type string
	Data map[string]any
}

func (o *TestObserver) record(typ string, data map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Events = append(o.Events, Event{Type: typ, Data: data})
}

// EventsByType returns all recorded events of the given type.
func (o *TestObserver) EventsByType(typ string) []Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []Event
	for _, e := range o.Events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnSourceConnected(sourceID, sourceType string) {
	o.record("SourceConnected", map[string]any{"sourceID": sourceID, "sourceType": sourceType})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnSourceDisconnected(sourceID, reason string) {
	o.record("SourceDisconnected", map[string]any{"sourceID": sourceID, "reason": reason})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnPipelineStateChanged(pipelineID string, oldState, newState laredo.PipelineState) {
	o.record("PipelineStateChanged", map[string]any{"pipelineID": pipelineID, "oldState": oldState, "newState": newState})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnBaselineStarted(pipelineID string, table laredo.TableIdentifier) {
	o.record("BaselineStarted", map[string]any{"pipelineID": pipelineID, "table": table})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnBaselineRowLoaded(pipelineID string, table laredo.TableIdentifier, rowCount int64) {
	o.record("BaselineRowLoaded", map[string]any{"pipelineID": pipelineID, "table": table, "rowCount": rowCount})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnBaselineCompleted(pipelineID string, table laredo.TableIdentifier, totalRows int64, duration time.Duration) {
	o.record("BaselineCompleted", map[string]any{"pipelineID": pipelineID, "table": table, "totalRows": totalRows, "duration": duration})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnChangeReceived(pipelineID string, table laredo.TableIdentifier, action laredo.ChangeAction, position laredo.Position) {
	o.record("ChangeReceived", map[string]any{"pipelineID": pipelineID, "table": table, "action": action, "position": position})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnChangeApplied(pipelineID string, table laredo.TableIdentifier, action laredo.ChangeAction, duration time.Duration) {
	o.record("ChangeApplied", map[string]any{"pipelineID": pipelineID, "table": table, "action": action, "duration": duration})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnChangeError(pipelineID string, table laredo.TableIdentifier, action laredo.ChangeAction, err laredo.ErrorInfo) {
	o.record("ChangeError", map[string]any{"pipelineID": pipelineID, "table": table, "action": action, "error": err})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnAckAdvanced(sourceID string, position laredo.Position) {
	o.record("AckAdvanced", map[string]any{"sourceID": sourceID, "position": position})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnBufferDepthChanged(pipelineID string, depth, capacity int) {
	o.record("BufferDepthChanged", map[string]any{"pipelineID": pipelineID, "depth": depth, "capacity": capacity})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnBufferPolicyTriggered(pipelineID string, policy laredo.BufferPolicy) {
	o.record("BufferPolicyTriggered", map[string]any{"pipelineID": pipelineID, "policy": policy})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnSnapshotStarted(snapshotID string) {
	o.record("SnapshotStarted", map[string]any{"snapshotID": snapshotID})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnSnapshotCompleted(snapshotID string, tables int, rows, sizeBytes int64, duration time.Duration) {
	o.record("SnapshotCompleted", map[string]any{"snapshotID": snapshotID, "tables": tables, "rows": rows, "sizeBytes": sizeBytes, "duration": duration})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnSnapshotFailed(snapshotID string, err laredo.ErrorInfo) {
	o.record("SnapshotFailed", map[string]any{"snapshotID": snapshotID, "error": err})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnSnapshotRestoreStarted(snapshotID string) {
	o.record("SnapshotRestoreStarted", map[string]any{"snapshotID": snapshotID})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnSnapshotRestoreCompleted(snapshotID string, duration time.Duration) {
	o.record("SnapshotRestoreCompleted", map[string]any{"snapshotID": snapshotID, "duration": duration})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnSchemaChange(sourceID string, table laredo.TableIdentifier, change laredo.SchemaChangeEvent) {
	o.record("SchemaChange", map[string]any{"sourceID": sourceID, "table": table, "change": change})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnLagUpdated(sourceID string, lagBytes int64, lagTime *time.Duration) {
	o.record("LagUpdated", map[string]any{"sourceID": sourceID, "lagBytes": lagBytes, "lagTime": lagTime})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnDeadLetterWritten(pipelineID string, change laredo.ChangeEvent, err laredo.ErrorInfo) {
	o.record("DeadLetterWritten", map[string]any{"pipelineID": pipelineID, "change": change, "error": err})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnRowExpired(pipelineID string, table laredo.TableIdentifier, key string) {
	o.record("RowExpired", map[string]any{"pipelineID": pipelineID, "table": table, "key": key})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnValidationResult(pipelineID string, table laredo.TableIdentifier, sourceCount, targetCount int64, match bool) {
	o.record("ValidationResult", map[string]any{"pipelineID": pipelineID, "table": table, "sourceCount": sourceCount, "targetCount": targetCount, "match": match})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnFanOutClientConnected(pipelineID, clientID string, syncMode laredo.SyncMode) {
	o.record("FanOutClientConnected", map[string]any{"pipelineID": pipelineID, "clientID": clientID, "syncMode": syncMode})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnFanOutClientDisconnected(pipelineID, clientID, reason string) {
	o.record("FanOutClientDisconnected", map[string]any{"pipelineID": pipelineID, "clientID": clientID, "reason": reason})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnFanOutClientCaughtUp(pipelineID, clientID string, sequence int64) {
	o.record("FanOutClientCaughtUp", map[string]any{"pipelineID": pipelineID, "clientID": clientID, "sequence": sequence})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnFanOutClientBackpressure(pipelineID, clientID string, bufferDepth int) {
	o.record("FanOutClientBackpressure", map[string]any{"pipelineID": pipelineID, "clientID": clientID, "bufferDepth": bufferDepth})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnFanOutSnapshotCreated(pipelineID, snapshotID string, sequence, rows int64) {
	o.record("FanOutSnapshotCreated", map[string]any{"pipelineID": pipelineID, "snapshotID": snapshotID, "sequence": sequence, "rows": rows})
}

//nolint:revive // implements EngineObserver.
func (o *TestObserver) OnFanOutJournalPruned(pipelineID string, entriesPruned, oldestSequence int64) {
	o.record("FanOutJournalPruned", map[string]any{"pipelineID": pipelineID, "entriesPruned": entriesPruned, "oldestSequence": oldestSequence})
}

// EventCount returns the number of recorded events of the given type.
func (o *TestObserver) EventCount(typ string) int {
	return len(o.EventsByType(typ))
}

// Reset clears all recorded events.
func (o *TestObserver) Reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Events = nil
}

// Verify that TestObserver implements EngineObserver at compile time.
var _ laredo.EngineObserver = (*TestObserver)(nil)
