package testutil

import (
	"testing"

	"github.com/zourzouvillys/laredo"
)

func TestTestObserver_RecordsEvents(t *testing.T) {
	obs := &TestObserver{}

	obs.OnSourceConnected("pg_main", "postgresql")
	obs.OnSourceConnected("kinesis", "s3-kinesis")
	obs.OnSourceDisconnected("pg_main", "shutdown")

	if len(obs.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(obs.Events))
	}

	connected := obs.EventsByType("SourceConnected")
	if len(connected) != 2 {
		t.Fatalf("expected 2 SourceConnected events, got %d", len(connected))
	}
	if connected[0].Data["sourceID"] != "pg_main" {
		t.Errorf("expected sourceID=pg_main, got %v", connected[0].Data["sourceID"])
	}
}

func TestTestObserver_EventCount(t *testing.T) {
	obs := &TestObserver{}

	obs.OnSourceConnected("a", "pg")
	obs.OnSourceConnected("b", "pg")
	obs.OnSourceDisconnected("a", "done")

	if got := obs.EventCount("SourceConnected"); got != 2 {
		t.Errorf("EventCount(SourceConnected) = %d, want 2", got)
	}
	if got := obs.EventCount("SourceDisconnected"); got != 1 {
		t.Errorf("EventCount(SourceDisconnected) = %d, want 1", got)
	}
	if got := obs.EventCount("NonExistent"); got != 0 {
		t.Errorf("EventCount(NonExistent) = %d, want 0", got)
	}
}

func TestTestObserver_Reset(t *testing.T) {
	obs := &TestObserver{}
	obs.OnSourceConnected("a", "pg")
	obs.Reset()

	if len(obs.Events) != 0 {
		t.Errorf("expected 0 events after Reset, got %d", len(obs.Events))
	}
}

func TestTestObserver_PipelineStateChanged(t *testing.T) {
	obs := &TestObserver{}
	obs.OnPipelineStateChanged("pipe1", laredo.PipelineInitializing, laredo.PipelineStreaming)

	events := obs.EventsByType("PipelineStateChanged")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data["pipelineID"] != "pipe1" {
		t.Errorf("expected pipelineID=pipe1, got %v", events[0].Data["pipelineID"])
	}
	if events[0].Data["oldState"] != laredo.PipelineInitializing {
		t.Errorf("expected oldState=INITIALIZING, got %v", events[0].Data["oldState"])
	}
	if events[0].Data["newState"] != laredo.PipelineStreaming {
		t.Errorf("expected newState=STREAMING, got %v", events[0].Data["newState"])
	}
}
