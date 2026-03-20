package laredo

import (
	"testing"
	"time"
)

// miniObserver is a minimal recording observer for testing CompositeObserver fan-out.
type miniObserver struct {
	calls []string
}

var _ EngineObserver = (*miniObserver)(nil)

func (o *miniObserver) record(name string) { o.calls = append(o.calls, name) }

func (o *miniObserver) OnSourceConnected(string, string)    { o.record("SourceConnected") }
func (o *miniObserver) OnSourceDisconnected(string, string) { o.record("SourceDisconnected") }
func (o *miniObserver) OnPipelineStateChanged(string, PipelineState, PipelineState) {
	o.record("PipelineStateChanged")
}
func (o *miniObserver) OnBaselineStarted(string, TableIdentifier) { o.record("BaselineStarted") }
func (o *miniObserver) OnBaselineRowLoaded(string, TableIdentifier, int64) {
	o.record("BaselineRowLoaded")
}

func (o *miniObserver) OnBaselineCompleted(string, TableIdentifier, int64, time.Duration) {
	o.record("BaselineCompleted")
}

func (o *miniObserver) OnChangeReceived(string, TableIdentifier, ChangeAction, Position) {
	o.record("ChangeReceived")
}

func (o *miniObserver) OnChangeApplied(string, TableIdentifier, ChangeAction, time.Duration) {
	o.record("ChangeApplied")
}

func (o *miniObserver) OnChangeError(string, TableIdentifier, ChangeAction, ErrorInfo) {
	o.record("ChangeError")
}
func (o *miniObserver) OnAckAdvanced(string, Position)        { o.record("AckAdvanced") }
func (o *miniObserver) OnBufferDepthChanged(string, int, int) { o.record("BufferDepthChanged") }
func (o *miniObserver) OnBufferPolicyTriggered(string, BufferPolicy) {
	o.record("BufferPolicyTriggered")
}
func (o *miniObserver) OnSnapshotStarted(string) { o.record("SnapshotStarted") }
func (o *miniObserver) OnSnapshotCompleted(string, int, int64, int64, time.Duration) {
	o.record("SnapshotCompleted")
}
func (o *miniObserver) OnSnapshotFailed(string, ErrorInfo) { o.record("SnapshotFailed") }
func (o *miniObserver) OnSnapshotRestoreStarted(string)    { o.record("SnapshotRestoreStarted") }
func (o *miniObserver) OnSnapshotRestoreCompleted(string, time.Duration) {
	o.record("SnapshotRestoreCompleted")
}

func (o *miniObserver) OnSchemaChange(string, TableIdentifier, SchemaChangeEvent) {
	o.record("SchemaChange")
}
func (o *miniObserver) OnLagUpdated(string, int64, *time.Duration) { o.record("LagUpdated") }
func (o *miniObserver) OnDeadLetterWritten(string, ChangeEvent, ErrorInfo) {
	o.record("DeadLetterWritten")
}
func (o *miniObserver) OnRowExpired(string, TableIdentifier, string) { o.record("RowExpired") }
func (o *miniObserver) OnValidationResult(string, TableIdentifier, int64, int64, bool) {
	o.record("ValidationResult")
}

func (o *miniObserver) OnFanOutClientConnected(string, string, SyncMode) {
	o.record("FanOutClientConnected")
}

func (o *miniObserver) OnFanOutClientDisconnected(string, string, string) {
	o.record("FanOutClientDisconnected")
}

func (o *miniObserver) OnFanOutClientCaughtUp(string, string, int64) {
	o.record("FanOutClientCaughtUp")
}

func (o *miniObserver) OnFanOutClientBackpressure(string, string, int) {
	o.record("FanOutClientBackpressure")
}

func (o *miniObserver) OnFanOutSnapshotCreated(string, string, int64, int64) {
	o.record("FanOutSnapshotCreated")
}
func (o *miniObserver) OnFanOutJournalPruned(string, int64, int64) { o.record("FanOutJournalPruned") }

func TestNullObserver_Interface(t *testing.T) {
	// Compile-time check is via the var _ line in observer.go.
	// Verify all methods can be called without panic.
	var n NullObserver
	table := Table("public", "t")
	n.OnSourceConnected("s", "pg")
	n.OnSourceDisconnected("s", "reason")
	n.OnPipelineStateChanged("p", PipelineInitializing, PipelineBaselining)
	n.OnBaselineStarted("p", table)
	n.OnBaselineRowLoaded("p", table, 100)
	n.OnBaselineCompleted("p", table, 100, time.Second)
	n.OnChangeReceived("p", table, ActionInsert, 1)
	n.OnChangeApplied("p", table, ActionInsert, time.Millisecond)
	n.OnChangeError("p", table, ActionInsert, ErrorInfo{})
	n.OnAckAdvanced("s", 1)
	n.OnBufferDepthChanged("p", 5, 100)
	n.OnBufferPolicyTriggered("p", BufferBlock)
	n.OnSnapshotStarted("snap1")
	n.OnSnapshotCompleted("snap1", 1, 100, 1024, time.Second)
	n.OnSnapshotFailed("snap1", ErrorInfo{})
	n.OnSnapshotRestoreStarted("snap1")
	n.OnSnapshotRestoreCompleted("snap1", time.Second)
	n.OnSchemaChange("s", table, SchemaChangeEvent{})
	n.OnLagUpdated("s", 1024, nil)
	n.OnDeadLetterWritten("p", ChangeEvent{}, ErrorInfo{})
	n.OnRowExpired("p", table, "key1")
	n.OnValidationResult("p", table, 100, 100, true)
	n.OnFanOutClientConnected("p", "c1", SyncModeFullSnapshot)
	n.OnFanOutClientDisconnected("p", "c1", "done")
	n.OnFanOutClientCaughtUp("p", "c1", 42)
	n.OnFanOutClientBackpressure("p", "c1", 50)
	n.OnFanOutSnapshotCreated("p", "snap1", 42, 100)
	n.OnFanOutJournalPruned("p", 10, 5)
}

func TestCompositeObserver_FansOut(t *testing.T) {
	o1 := &miniObserver{}
	o2 := &miniObserver{}
	c := NewCompositeObserver(o1, o2)

	c.OnSourceConnected("src", "pg")
	c.OnBaselineStarted("pipe", Table("public", "t"))
	c.OnChangeApplied("pipe", Table("public", "t"), ActionInsert, time.Millisecond)

	for _, o := range []*miniObserver{o1, o2} {
		if len(o.calls) != 3 {
			t.Fatalf("expected 3 calls, got %d: %v", len(o.calls), o.calls)
		}
		if o.calls[0] != "SourceConnected" || o.calls[1] != "BaselineStarted" || o.calls[2] != "ChangeApplied" {
			t.Errorf("unexpected calls: %v", o.calls)
		}
	}
}

func TestCompositeObserver_Empty(t *testing.T) {
	c := NewCompositeObserver()
	// Should not panic with zero observers.
	c.OnSourceConnected("s", "pg")
	c.OnBaselineStarted("p", Table("public", "t"))
	c.OnChangeReceived("p", Table("public", "t"), ActionInsert, 1)
}

func TestCompositeObserver_NilFiltered(t *testing.T) {
	o := &miniObserver{}
	c := NewCompositeObserver(nil, o, nil)

	c.OnSourceConnected("s", "pg")

	if len(o.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(o.calls))
	}
	if o.calls[0] != "SourceConnected" {
		t.Errorf("unexpected call: %s", o.calls[0])
	}
}
