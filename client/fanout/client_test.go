package fanout

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
)

// testServer is a programmable replication server for testing.
type testServer struct {
	replicationv1connect.UnimplementedLaredoReplicationServiceHandler
	mu      sync.Mutex
	syncFn  func(context.Context, *connect.Request[v1.SyncRequest], *connect.ServerStream[v1.SyncResponse]) error
	syncCnt atomic.Int32
}

func (s *testServer) Sync(ctx context.Context, req *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
	s.syncCnt.Add(1)
	s.mu.Lock()
	fn := s.syncFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, req, stream)
	}
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("not configured"))
}

func (s *testServer) setSyncFn(fn func(context.Context, *connect.Request[v1.SyncRequest], *connect.ServerStream[v1.SyncResponse]) error) {
	s.mu.Lock()
	s.syncFn = fn
	s.mu.Unlock()
}

// startTestServer starts a test HTTP server with the replication handler and
// returns the address (host:port) and a cleanup function.
func startTestServer(t *testing.T, svc *testServer) string {
	t.Helper()
	path, handler := replicationv1connect.NewLaredoReplicationServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	return listener.Addr().String()
}

// makeRow creates a structpb.Struct from a map.
func makeRow(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}

func TestClient_FullSnapshot(t *testing.T) {
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, req *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		// Verify request fields.
		if req.Msg.GetSchema() != "public" || req.Msg.GetTable() != "users" {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unexpected table"))
		}

		// Handshake.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{
					Mode:                  v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT,
					ServerCurrentSequence: 3,
					JournalOldestSequence: 1,
				},
			},
		}); err != nil {
			return err
		}

		// Snapshot.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotBegin{
				SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "snap-1", Sequence: 3, RowCount: 2},
			},
		}); err != nil {
			return err
		}

		for i := range 2 {
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotRow{
					SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{
						"id":   fmt.Sprintf("%d", i+1),
						"name": fmt.Sprintf("user-%d", i+1),
					})},
				},
			}); err != nil {
				return err
			}
		}

		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotEnd{
				SnapshotEnd: &v1.SnapshotEnd{Sequence: 3, RowsSent: 2},
			},
		}); err != nil {
			return err
		}

		// Keep alive until context cancelled.
		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "users"), ClientID("test-1"))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)

	if !c.AwaitReady(5 * time.Second) {
		t.Fatal("client not ready")
	}

	if c.Count() != 2 {
		t.Errorf("expected 2 rows, got %d", c.Count())
	}

	row, ok := c.Get("1")
	if !ok {
		t.Fatal("row 1 not found")
	}
	if row["name"] != "user-1" {
		t.Errorf("expected name=user-1, got %v", row["name"])
	}

	if c.LastSequence() != 3 {
		t.Errorf("expected lastSeq=3, got %d", c.LastSequence())
	}
}

func TestClient_Delta(t *testing.T) {
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, req *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		lastSeq := req.Msg.GetLastKnownSequence()
		if lastSeq != 5 {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("expected lastSeq=5, got %d", lastSeq))
		}

		// Handshake — delta mode.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{
					Mode:                  v1.SyncMode_SYNC_MODE_DELTA,
					ServerCurrentSequence: 7,
					JournalOldestSequence: 1,
					ResumeFromSequence:    5,
				},
			},
		}); err != nil {
			return err
		}

		// Journal entries 6 and 7.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence:  6,
					Timestamp: timestamppb.Now(),
					Action:    "INSERT",
					NewValues: makeRow(t, map[string]any{"id": "10", "name": "new-user"}),
				},
			},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence:  7,
					Timestamp: timestamppb.Now(),
					Action:    "UPDATE",
					NewValues: makeRow(t, map[string]any{"id": "1", "name": "updated"}),
				},
			},
		}); err != nil {
			return err
		}

		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "users"), ClientID("delta-1"))
	// Pre-populate store to simulate previous session.
	c.store["1"] = map[string]any{"id": "1", "name": "original"}
	c.lastSeq = 5

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)

	if !c.AwaitReady(5 * time.Second) {
		t.Fatal("client not ready after delta")
	}

	// INSERT added row 10.
	row, ok := c.Get("10")
	if !ok {
		t.Fatal("row 10 not found after INSERT")
	}
	if row["name"] != "new-user" {
		t.Errorf("expected name=new-user, got %v", row["name"])
	}

	// UPDATE changed row 1.
	row, ok = c.Get("1")
	if !ok {
		t.Fatal("row 1 not found after UPDATE")
	}
	if row["name"] != "updated" {
		t.Errorf("expected name=updated, got %v", row["name"])
	}

	if c.LastSequence() != 7 {
		t.Errorf("expected lastSeq=7, got %d", c.LastSequence())
	}
}

func TestClient_JournalActions(t *testing.T) {
	// Tests INSERT, UPDATE, DELETE, TRUNCATE journal actions and listener.
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, _ *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		// Handshake + empty snapshot.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, ServerCurrentSequence: 0},
			},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotBegin{SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", Sequence: 0, RowCount: 0}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotEnd{SnapshotEnd: &v1.SnapshotEnd{Sequence: 0, RowsSent: 0}},
		}); err != nil {
			return err
		}

		// INSERT.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence: 1, Action: "INSERT",
					NewValues: makeRow(t, map[string]any{"id": "1", "name": "alice"}),
				},
			},
		}); err != nil {
			return err
		}

		// UPDATE.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence: 2, Action: "UPDATE",
					NewValues: makeRow(t, map[string]any{"id": "1", "name": "bob"}),
				},
			},
		}); err != nil {
			return err
		}

		// Another INSERT so we have 2 rows.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence: 3, Action: "INSERT",
					NewValues: makeRow(t, map[string]any{"id": "2", "name": "carol"}),
				},
			},
		}); err != nil {
			return err
		}

		// DELETE row 1.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence: 4, Action: "DELETE",
					OldValues: makeRow(t, map[string]any{"id": "1", "name": "bob"}),
				},
			},
		}); err != nil {
			return err
		}

		// TRUNCATE.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence: 5, Action: "TRUNCATE",
				},
			},
		}); err != nil {
			return err
		}

		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "t"), ClientID("actions-1"))

	// Track listener events.
	var events []string
	var eventMu sync.Mutex

	unsub := c.Listen(func(old, new laredo.Row) {
		eventMu.Lock()
		defer eventMu.Unlock()
		switch {
		case old == nil && new != nil:
			events = append(events, fmt.Sprintf("INSERT:%v", new["id"]))
		case old != nil && new != nil:
			events = append(events, fmt.Sprintf("UPDATE:%v", new["id"]))
		case old != nil && new == nil:
			events = append(events, fmt.Sprintf("DELETE:%v", old["id"]))
		default:
			events = append(events, "TRUNCATE")
		}
	})
	defer unsub()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)

	// Wait for all journal entries to be processed.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.LastSequence() >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c.LastSequence() < 5 {
		t.Fatalf("expected lastSeq>=5, got %d", c.LastSequence())
	}

	// After TRUNCATE, store should be empty.
	if c.Count() != 0 {
		t.Errorf("expected 0 rows after TRUNCATE, got %d", c.Count())
	}

	eventMu.Lock()
	got := events
	eventMu.Unlock()

	want := []string{"INSERT:1", "UPDATE:1", "INSERT:2", "DELETE:1", "TRUNCATE"}
	if len(got) != len(want) {
		t.Fatalf("expected %d events, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d]: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestClient_LookupAndAll(t *testing.T) {
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, _ *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, ServerCurrentSequence: 2},
			},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotBegin{SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", Sequence: 2, RowCount: 2}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotRow{SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"id": "1", "email": "a@b.com"})}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotRow{SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"id": "2", "email": "c@d.com"})}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotEnd{SnapshotEnd: &v1.SnapshotEnd{Sequence: 2, RowsSent: 2}},
		}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "t"))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)

	if !c.AwaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}

	// Lookup by email.
	row, ok := c.Lookup("email", "c@d.com")
	if !ok {
		t.Fatal("lookup by email failed")
	}
	if row["id"] != "2" {
		t.Errorf("expected id=2, got %v", row["id"])
	}

	// Lookup miss.
	_, ok = c.Lookup("email", "missing@x.com")
	if ok {
		t.Error("expected lookup miss")
	}

	// All rows.
	all := c.All()
	if len(all) != 2 {
		t.Errorf("expected 2 rows from All(), got %d", len(all))
	}
}

func TestClient_Reconnect(t *testing.T) {
	ts := &testServer{}

	// First call: send snapshot then close the stream.
	// Second call: send delta then keep alive.
	ts.setSyncFn(func(ctx context.Context, req *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		callNum := ts.syncCnt.Load()

		if callNum == 1 {
			// First connection: full snapshot, then abrupt close.
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_Handshake{
					Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, ServerCurrentSequence: 2},
				},
			}); err != nil {
				return err
			}
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotBegin{SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", Sequence: 2, RowCount: 1}},
			}); err != nil {
				return err
			}
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotRow{SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"id": "1", "name": "a"})}},
			}); err != nil {
				return err
			}
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotEnd{SnapshotEnd: &v1.SnapshotEnd{Sequence: 2, RowsSent: 1}},
			}); err != nil {
				return err
			}

			// Simulate abrupt connection close (return error).
			return fmt.Errorf("simulated disconnect")
		}

		// Second connection: delta with new data.
		lastSeq := req.Msg.GetLastKnownSequence()
		if lastSeq != 2 {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("expected lastSeq=2 on reconnect, got %d", lastSeq))
		}

		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{
					Mode:                  v1.SyncMode_SYNC_MODE_DELTA,
					ServerCurrentSequence: 3,
					JournalOldestSequence: 1,
					ResumeFromSequence:    2,
				},
			},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence: 3, Action: "INSERT",
					NewValues: makeRow(t, map[string]any{"id": "2", "name": "b"}),
				},
			},
		}); err != nil {
			return err
		}

		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "t"), ClientID("reconnect-1"))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)

	// Wait for reconnect and row 2 to appear.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c.Count() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if c.Count() != 2 {
		t.Errorf("expected 2 rows after reconnect, got %d", c.Count())
	}

	// Original row preserved.
	row, ok := c.Get("1")
	if !ok {
		t.Fatal("row 1 lost after reconnect")
	}
	if row["name"] != "a" {
		t.Errorf("row 1 name: expected a, got %v", row["name"])
	}

	// New row from delta.
	row, ok = c.Get("2")
	if !ok {
		t.Fatal("row 2 not found after reconnect")
	}
	if row["name"] != "b" {
		t.Errorf("row 2 name: expected b, got %v", row["name"])
	}

	if c.LastSequence() != 3 {
		t.Errorf("expected lastSeq=3, got %d", c.LastSequence())
	}

	if ts.syncCnt.Load() < 2 {
		t.Errorf("expected at least 2 sync calls (reconnect), got %d", ts.syncCnt.Load())
	}
}

func TestClient_SnapshotClearsStore(t *testing.T) {
	// Verify that a full snapshot clears any existing data.
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, _ *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, ServerCurrentSequence: 1},
			},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotBegin{SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", Sequence: 1, RowCount: 1}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotRow{SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"id": "new", "val": "x"})}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotEnd{SnapshotEnd: &v1.SnapshotEnd{Sequence: 1, RowsSent: 1}},
		}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "t"))
	// Pre-populate with stale data.
	c.store["old"] = map[string]any{"id": "old", "val": "stale"}

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)

	if !c.AwaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}

	// Old data should be gone, only new snapshot data present.
	if c.Count() != 1 {
		t.Errorf("expected 1 row, got %d", c.Count())
	}
	_, ok := c.Get("old")
	if ok {
		t.Error("stale row 'old' should have been cleared by snapshot")
	}
	_, ok = c.Get("new")
	if !ok {
		t.Error("new row not found after snapshot")
	}
}

func TestClient_Heartbeat(t *testing.T) {
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, _ *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, ServerCurrentSequence: 0},
			},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotBegin{SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", Sequence: 0, RowCount: 0}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotEnd{SnapshotEnd: &v1.SnapshotEnd{Sequence: 0, RowsSent: 0}},
		}); err != nil {
			return err
		}

		// Send heartbeat.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Heartbeat{
				Heartbeat: &v1.Heartbeat{
					CurrentSequence: 0,
					ServerTime:      timestamppb.Now(),
				},
			},
		}); err != nil {
			return err
		}

		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "t"))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)

	if !c.AwaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}

	// Give time for heartbeat to be received.
	time.Sleep(100 * time.Millisecond)

	c.mu.RLock()
	lr := c.lastReceived
	c.mu.RUnlock()

	if lr.IsZero() {
		t.Error("lastReceived not updated after heartbeat")
	}
	if time.Since(lr) > 2*time.Second {
		t.Errorf("lastReceived too old: %v ago", time.Since(lr))
	}
}

func TestClient_StopBeforeReady(t *testing.T) {
	// Server that never sends anything useful — just hangs.
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, _ *connect.Request[v1.SyncRequest], _ *connect.ServerStream[v1.SyncResponse]) error {
		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "t"))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// AwaitReady should time out.
	if c.AwaitReady(200 * time.Millisecond) {
		t.Error("expected AwaitReady to timeout")
	}

	// Stop should return promptly.
	done := make(chan struct{})
	go func() {
		c.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s")
	}
}

func TestClient_LocalSnapshot(t *testing.T) {
	// Tests save/restore of local snapshot: first client populates data,
	// second client loads it and requests delta instead of full snapshot.
	ts := &testServer{}

	// First connection: full snapshot with 2 rows.
	ts.setSyncFn(func(ctx context.Context, req *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		callNum := ts.syncCnt.Load()

		if callNum == 1 {
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_Handshake{
					Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, ServerCurrentSequence: 5},
				},
			}); err != nil {
				return err
			}
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotBegin{SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", Sequence: 5, RowCount: 2}},
			}); err != nil {
				return err
			}
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotRow{SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"id": "1", "name": "a"})}},
			}); err != nil {
				return err
			}
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotRow{SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"id": "2", "name": "b"})}},
			}); err != nil {
				return err
			}
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotEnd{SnapshotEnd: &v1.SnapshotEnd{Sequence: 5, RowsSent: 2}},
			}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}

		// Second connection: verify client sends lastSeq=5 (loaded from snapshot).
		lastSeq := req.Msg.GetLastKnownSequence()
		if lastSeq != 5 {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("expected lastSeq=5, got %d", lastSeq))
		}

		// Send delta with one new row.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{
					Mode: v1.SyncMode_SYNC_MODE_DELTA, ServerCurrentSequence: 6,
					JournalOldestSequence: 1, ResumeFromSequence: 5,
				},
			},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence: 6, Action: "INSERT",
					NewValues: makeRow(t, map[string]any{"id": "3", "name": "c"}),
				},
			},
		}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)
	snapPath := filepath.Join(t.TempDir(), "snapshot.json")

	// First client: populate data, then stop (saves snapshot).
	c1 := New(ServerAddress(addr), Table("public", "t"), LocalSnapshotPath(snapPath))
	ctx := context.Background()
	if err := c1.Start(ctx); err != nil {
		t.Fatalf("start c1: %v", err)
	}
	if !c1.AwaitReady(5 * time.Second) {
		t.Fatal("c1 not ready")
	}
	if c1.Count() != 2 {
		t.Fatalf("c1: expected 2 rows, got %d", c1.Count())
	}
	c1.Stop() // Saves snapshot to snapPath.

	// Second client: loads snapshot, requests delta, gets 1 new row.
	c2 := New(ServerAddress(addr), Table("public", "t"), LocalSnapshotPath(snapPath))
	if err := c2.Start(ctx); err != nil {
		t.Fatalf("start c2: %v", err)
	}
	t.Cleanup(c2.Stop)

	// Wait for delta to be applied.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c2.Count() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if c2.Count() != 3 {
		t.Errorf("c2: expected 3 rows, got %d", c2.Count())
	}
	if c2.LastSequence() != 6 {
		t.Errorf("c2: expected lastSeq=6, got %d", c2.LastSequence())
	}

	// Verify original rows were loaded from snapshot.
	row, ok := c2.Get("1")
	if !ok {
		t.Fatal("row 1 not found (should come from snapshot)")
	}
	if row["name"] != "a" {
		t.Errorf("row 1: expected name=a, got %v", row["name"])
	}

	// Verify new row from delta.
	row, ok = c2.Get("3")
	if !ok {
		t.Fatal("row 3 not found (should come from delta)")
	}
	if row["name"] != "c" {
		t.Errorf("row 3: expected name=c, got %v", row["name"])
	}
}

func TestClient_LookupByIndex(t *testing.T) {
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, _ *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, ServerCurrentSequence: 3},
			},
		}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotBegin{SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", Sequence: 3, RowCount: 3}},
		}); err != nil {
			return err
		}
		for _, row := range []map[string]any{
			{"id": "1", "dept": "eng", "name": "alice"},
			{"id": "2", "dept": "eng", "name": "bob"},
			{"id": "3", "dept": "sales", "name": "carol"},
		} {
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotRow{SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, row)}},
			}); err != nil {
				return err
			}
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotEnd{SnapshotEnd: &v1.SnapshotEnd{Sequence: 3, RowsSent: 3}},
		}); err != nil {
			return err
		}

		// Send a journal entry: UPDATE bob's dept to sales.
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_JournalEntry{
				JournalEntry: &v1.ReplicationJournalEntry{
					Sequence: 4, Action: "UPDATE",
					NewValues: makeRow(t, map[string]any{"id": "2", "dept": "sales", "name": "bob"}),
					OldValues: makeRow(t, map[string]any{"id": "2", "dept": "eng", "name": "bob"}),
				},
			},
		}); err != nil {
			return err
		}

		<-ctx.Done()
		return ctx.Err()
	})

	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "t"), WithIndex("by_dept", "dept"))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)

	// Wait for all data including the UPDATE.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.LastSequence() >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// After snapshot: eng=[alice, bob], sales=[carol].
	// After UPDATE bob→sales: eng=[alice], sales=[carol, bob].
	eng := c.LookupByIndex("by_dept", "eng")
	if len(eng) != 1 {
		t.Errorf("expected 1 eng row after update, got %d", len(eng))
	}
	if len(eng) > 0 && eng[0]["name"] != "alice" {
		t.Errorf("expected alice in eng, got %v", eng[0]["name"])
	}

	sales := c.LookupByIndex("by_dept", "sales")
	if len(sales) != 2 {
		t.Errorf("expected 2 sales rows after update, got %d", len(sales))
	}

	// Lookup on non-existent index.
	result := c.LookupByIndex("nonexistent", "x")
	if result != nil {
		t.Error("expected nil for non-existent index")
	}

	// Lookup with no matches.
	result = c.LookupByIndex("by_dept", "hr")
	if result != nil {
		t.Error("expected nil for no matches")
	}
}

func TestRowKey(t *testing.T) {
	tests := []struct {
		name string
		row  map[string]any
		want string
	}{
		{
			name: "with id field",
			row:  map[string]any{"id": "42", "name": "test"},
			want: "42",
		},
		{
			name: "with numeric id",
			row:  map[string]any{"id": 7, "name": "test"},
			want: "7",
		},
		{
			name: "without id field",
			row:  map[string]any{"name": "test"},
			// Falls back to fmt.Sprintf of the whole map.
			want: fmt.Sprintf("%v", map[string]any{"name": "test"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rowKey(tt.row)
			if got != tt.want {
				t.Errorf("rowKey(%v) = %q, want %q", tt.row, got, tt.want)
			}
		})
	}
}
