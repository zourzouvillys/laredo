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
	Data map[string]interface{}
}

func (o *TestObserver) record(typ string, data map[string]interface{}) {
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

func (o *TestObserver) OnSourceConnected(sourceID, sourceType string) {
	o.record("SourceConnected", map[string]interface{}{"sourceID": sourceID, "sourceType": sourceType})
}

func (o *TestObserver) OnSourceDisconnected(sourceID, reason string) {
	o.record("SourceDisconnected", map[string]interface{}{"sourceID": sourceID, "reason": reason})
}

func (o *TestObserver) OnPipelineStateChanged(pipelineID string, oldState, newState laredo.PipelineState) {
	o.record("PipelineStateChanged", map[string]interface{}{"pipelineID": pipelineID, "oldState": oldState, "newState": newState})
}

func (o *TestObserver) OnBaselineStarted(pipelineID string, table laredo.TableIdentifier) {
	o.record("BaselineStarted", map[string]interface{}{"pipelineID": pipelineID, "table": table})
}

func (o *TestObserver) OnBaselineRowLoaded(pipelineID string, table laredo.TableIdentifier, rowCount int64) {
	o.record("BaselineRowLoaded", map[string]interface{}{"pipelineID": pipelineID, "table": table, "rowCount": rowCount})
}

func (o *TestObserver) OnBaselineCompleted(pipelineID string, table laredo.TableIdentifier, totalRows int64, duration time.Duration) {
	o.record("BaselineCompleted", map[string]interface{}{"pipelineID": pipelineID, "table": table, "totalRows": totalRows, "duration": duration})
}

func (o *TestObserver) OnChangeReceived(pipelineID string, table laredo.TableIdentifier, action laredo.ChangeAction, position laredo.Position) {
	o.record("ChangeReceived", map[string]interface{}{"pipelineID": pipelineID, "table": table, "action": action, "position": position})
}

func (o *TestObserver) OnChangeApplied(pipelineID string, table laredo.TableIdentifier, action laredo.ChangeAction, duration time.Duration) {
	o.record("ChangeApplied", map[string]interface{}{"pipelineID": pipelineID, "table": table, "action": action, "duration": duration})
}

func (o *TestObserver) OnChangeError(pipelineID string, table laredo.TableIdentifier, action laredo.ChangeAction, err laredo.ErrorInfo) {
	o.record("ChangeError", map[string]interface{}{"pipelineID": pipelineID, "table": table, "action": action, "error": err})
}

func (o *TestObserver) OnAckAdvanced(sourceID string, position laredo.Position) {
	o.record("AckAdvanced", map[string]interface{}{"sourceID": sourceID, "position": position})
}

func (o *TestObserver) OnBufferDepthChanged(pipelineID string, depth, capacity int) {
	o.record("BufferDepthChanged", map[string]interface{}{"pipelineID": pipelineID, "depth": depth, "capacity": capacity})
}

func (o *TestObserver) OnBufferPolicyTriggered(pipelineID string, policy laredo.BufferPolicy) {
	o.record("BufferPolicyTriggered", map[string]interface{}{"pipelineID": pipelineID, "policy": policy})
}

func (o *TestObserver) OnSnapshotStarted(snapshotID string) {
	o.record("SnapshotStarted", map[string]interface{}{"snapshotID": snapshotID})
}

func (o *TestObserver) OnSnapshotCompleted(snapshotID string, tables int, rows, sizeBytes int64, duration time.Duration) {
	o.record("SnapshotCompleted", map[string]interface{}{"snapshotID": snapshotID, "tables": tables, "rows": rows, "sizeBytes": sizeBytes, "duration": duration})
}

func (o *TestObserver) OnSnapshotFailed(snapshotID string, err laredo.ErrorInfo) {
	o.record("SnapshotFailed", map[string]interface{}{"snapshotID": snapshotID, "error": err})
}

func (o *TestObserver) OnSnapshotRestoreStarted(snapshotID string) {
	o.record("SnapshotRestoreStarted", map[string]interface{}{"snapshotID": snapshotID})
}

func (o *TestObserver) OnSnapshotRestoreCompleted(snapshotID string, duration time.Duration) {
	o.record("SnapshotRestoreCompleted", map[string]interface{}{"snapshotID": snapshotID, "duration": duration})
}

func (o *TestObserver) OnSchemaChange(sourceID string, table laredo.TableIdentifier, change laredo.SchemaChangeEvent) {
	o.record("SchemaChange", map[string]interface{}{"sourceID": sourceID, "table": table, "change": change})
}

func (o *TestObserver) OnLagUpdated(sourceID string, lagBytes int64, lagTime *time.Duration) {
	o.record("LagUpdated", map[string]interface{}{"sourceID": sourceID, "lagBytes": lagBytes, "lagTime": lagTime})
}

func (o *TestObserver) OnDeadLetterWritten(pipelineID string, change laredo.ChangeEvent, err laredo.ErrorInfo) {
	o.record("DeadLetterWritten", map[string]interface{}{"pipelineID": pipelineID, "change": change, "error": err})
}

func (o *TestObserver) OnRowExpired(pipelineID string, table laredo.TableIdentifier, key string) {
	o.record("RowExpired", map[string]interface{}{"pipelineID": pipelineID, "table": table, "key": key})
}

func (o *TestObserver) OnValidationResult(pipelineID string, table laredo.TableIdentifier, sourceCount, targetCount int64, match bool) {
	o.record("ValidationResult", map[string]interface{}{"pipelineID": pipelineID, "table": table, "sourceCount": sourceCount, "targetCount": targetCount, "match": match})
}

func (o *TestObserver) OnFanOutClientConnected(pipelineID, clientID string, syncMode laredo.SyncMode) {
	o.record("FanOutClientConnected", map[string]interface{}{"pipelineID": pipelineID, "clientID": clientID, "syncMode": syncMode})
}

func (o *TestObserver) OnFanOutClientDisconnected(pipelineID, clientID, reason string) {
	o.record("FanOutClientDisconnected", map[string]interface{}{"pipelineID": pipelineID, "clientID": clientID, "reason": reason})
}

func (o *TestObserver) OnFanOutClientCaughtUp(pipelineID, clientID string, sequence int64) {
	o.record("FanOutClientCaughtUp", map[string]interface{}{"pipelineID": pipelineID, "clientID": clientID, "sequence": sequence})
}

func (o *TestObserver) OnFanOutClientBackpressure(pipelineID, clientID string, bufferDepth int) {
	o.record("FanOutClientBackpressure", map[string]interface{}{"pipelineID": pipelineID, "clientID": clientID, "bufferDepth": bufferDepth})
}

func (o *TestObserver) OnFanOutSnapshotCreated(pipelineID, snapshotID string, sequence, rows int64) {
	o.record("FanOutSnapshotCreated", map[string]interface{}{"pipelineID": pipelineID, "snapshotID": snapshotID, "sequence": sequence, "rows": rows})
}

func (o *TestObserver) OnFanOutJournalPruned(pipelineID string, entriesPruned, oldestSequence int64) {
	o.record("FanOutJournalPruned", map[string]interface{}{"pipelineID": pipelineID, "entriesPruned": entriesPruned, "oldestSequence": oldestSequence})
}

// Verify that TestObserver implements EngineObserver at compile time.
var _ laredo.EngineObserver = (*TestObserver)(nil)
