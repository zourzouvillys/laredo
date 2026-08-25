package fanout

import (
	"context"
	"testing"
	"time"

	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
)

// TestJitter_StaysWithinHalfToFull pins the reconnect spread. Without jitter,
// every subscriber of a draining server came back in lockstep; with too much,
// a reconnect could be delayed arbitrarily.
func TestJitter_StaysWithinHalfToFull(t *testing.T) {
	const d = 4 * time.Second
	for i := 0; i < 200; i++ {
		got := jitter(d)
		if got < d/2 || got > d {
			t.Fatalf("jitter(%v) = %v, want within [%v, %v]", d, got, d/2, d)
		}
	}
	if got := jitter(0); got != 0 {
		t.Errorf("jitter(0) = %v, want 0", got)
	}
}

// TestClientState_ReportsStalenessAndErrors covers the accessors that did not
// exist: the client recorded lastReceived and then read it nowhere, and stream
// errors were discarded entirely, so a consumer could not distinguish a quiet
// table from a client that had never connected.
func TestClientState_ReportsStalenessAndErrors(t *testing.T) {
	ts := &testServer{}
	ts.setSyncFn(snapshotThen(t, nil, nil))
	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "users"), ClientID("t"))

	// Before Start: nothing received, so stale and not connected.
	if !c.IsStale() {
		t.Error("IsStale() = false before the client has received anything")
	}
	if c.Connected() {
		t.Error("Connected() = true before Start")
	}
	if !c.LastReceived().IsZero() {
		t.Error("LastReceived() should be zero before the first message")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	if !c.AwaitReady(5 * time.Second) {
		t.Fatal("never became ready")
	}
	if c.IsStale() {
		t.Error("IsStale() = true immediately after a successful snapshot")
	}
	if !c.Connected() {
		t.Error("Connected() = false after a handshake")
	}
	if c.LastReceived().IsZero() {
		t.Error("LastReceived() still zero after receiving a snapshot")
	}
	if err := c.LastError(); err != nil {
		t.Errorf("LastError() = %v on a healthy client", err)
	}
}

// TestHeartbeat_AdvancesSourcePosition covers the idle-subscriber drift: the
// client ignored heartbeats entirely, so a subscriber that matched no journal
// entries kept resuming from an ever-staler position.
func TestHeartbeat_AdvancesSourcePosition(t *testing.T) {
	ts := &testServer{}
	ts.setSyncFn(snapshotThen(t, nil, func(stream *testStream) error {
		return stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_Heartbeat{
			Heartbeat: &v1.Heartbeat{CurrentSequence: 9, SourcePosition: "0/DEADBEEF"},
		}})
	}))
	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "users"), ClientID("t"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	if !c.AwaitReady(5 * time.Second) {
		t.Fatal("never became ready")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.LastSourcePosition() == "0/DEADBEEF" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("LastSourcePosition() = %q, want the position the heartbeat carried",
		c.LastSourcePosition())
}
