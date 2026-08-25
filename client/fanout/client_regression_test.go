package fanout

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
)

// sendJournal is a small helper for driving the client from a test server.
func sendJournal(stream *testStream, seq int64, action string, row map[string]any, t *testing.T) error {
	t.Helper()
	entry := &v1.ReplicationJournalEntry{
		Sequence:       seq,
		SourcePosition: fmt.Sprintf("0/%d", seq),
		Action:         action,
	}
	switch action {
	case "DELETE":
		entry.OldValues = makeRow(t, row)
	default:
		entry.NewValues = makeRow(t, row)
	}
	return stream.Send(&v1.SyncResponse{
		Message: &v1.SyncResponse_JournalEntry{JournalEntry: entry},
	})
}

// snapshotThen sends a one-row snapshot, then hands control to more.
func snapshotThen(t *testing.T, cols []*v1.ColumnDefinition, more func(*testStream) error) func(context.Context, *v1.SyncStart, *testStream) error {
	t.Helper()
	return func(ctx context.Context, _ *v1.SyncStart, stream *testStream) error {
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_Handshake{
			Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, Columns: cols},
		}}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotBegin{
			SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", Sequence: 1, RowCount: 1},
		}}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotRow{
			SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"id": "1", "name": "one"})},
		}}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotEnd{
			SnapshotEnd: &v1.SnapshotEnd{Sequence: 1, RowsSent: 1},
		}}); err != nil {
			return err
		}
		if more != nil {
			if err := more(stream); err != nil {
				return err
			}
		}
		<-ctx.Done()
		return nil
	}
}

// TestListener_MayReadTheClient is the regression test for the deadlock.
// applyJournalEntry used to hold the write lock across the callback, so a
// listener calling any accessor — all of which take RLock, and RWMutex is not
// reentrant — wedged the stream goroutine permanently. The symptom was not a
// panic or a race report but a client that simply stopped applying changes.
func TestListener_MayReadTheClient(t *testing.T) {
	ts := &testServer{}
	ts.setSyncFn(snapshotThen(t, nil, func(stream *testStream) error {
		return sendJournal(stream, 2, "INSERT", map[string]any{"id": "2", "name": "two"}, t)
	}))
	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "users"), ClientID("t"))

	got := make(chan int, 1)
	c.Listen(func(old, new laredo.Row) {
		// The whole point: reading the client from inside the callback.
		got <- c.Count()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	select {
	case n := <-got:
		if n != 2 {
			t.Errorf("Count() from inside the listener = %d, want 2", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener that read the client never returned — the stream goroutine is deadlocked")
	}
}

// TestListen_SupportsMultipleSubscribers covers the second half of the same
// defect: Listen assigned a single field, so a second subscriber silently
// replaced the first and the first's unsubscribe removed the second's.
func TestListen_SupportsMultipleSubscribers(t *testing.T) {
	ts := &testServer{}
	release := make(chan struct{})
	ts.setSyncFn(snapshotThen(t, nil, func(stream *testStream) error {
		if err := sendJournal(stream, 2, "INSERT", map[string]any{"id": "2"}, t); err != nil {
			return err
		}
		<-release
		return sendJournal(stream, 3, "INSERT", map[string]any{"id": "3"}, t)
	}))
	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "users"), ClientID("t"))

	var mu sync.Mutex
	var aCount, bCount int
	firstA := make(chan struct{}, 1)
	firstB := make(chan struct{}, 1)

	unsubA := c.Listen(func(old, new laredo.Row) {
		mu.Lock()
		aCount++
		mu.Unlock()
		select {
		case firstA <- struct{}{}:
		default:
		}
	})
	c.Listen(func(old, new laredo.Row) {
		mu.Lock()
		bCount++
		mu.Unlock()
		select {
		case firstB <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	// Both must see the first change.
	for _, ch := range []chan struct{}{firstA, firstB} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("a registered listener never fired; Listen is not multi-subscriber")
		}
	}

	// Unsubscribing A must leave B working.
	unsubA()
	mu.Lock()
	aAtUnsub := aCount
	mu.Unlock()

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		b := bCount
		mu.Unlock()
		if b >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if bCount < 2 {
		t.Errorf("second listener stopped after the first unsubscribed (b=%d)", bCount)
	}
	if aCount != aAtUnsub {
		t.Errorf("unsubscribed listener still fired (a went %d -> %d)", aAtUnsub, aCount)
	}
}

// TestResnapshot_NeverExposesPartialState covers the re-snapshot defect.
// SnapshotBegin used to wipe the store in place while ready stayed true, so a
// concurrent reader saw the replica drop to zero rows and refill one at a
// time. The replacement builds aside and swaps once.
func TestResnapshot_NeverExposesPartialState(t *testing.T) {
	const rows = 40
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, _ *v1.SyncStart, stream *testStream) error {
		send := func(id int) error {
			return stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotRow{
				SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"id": fmt.Sprint(id)})},
			}})
		}
		for round := 0; round < 2; round++ {
			if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_Handshake{
				Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT},
			}}); err != nil {
				return err
			}
			if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotBegin{
				SnapshotBegin: &v1.SnapshotBegin{SnapshotId: fmt.Sprintf("s%d", round), RowCount: rows},
			}}); err != nil {
				return err
			}
			for i := 0; i < rows; i++ {
				if err := send(i); err != nil {
					return err
				}
				time.Sleep(time.Millisecond)
			}
			if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotEnd{
				SnapshotEnd: &v1.SnapshotEnd{Sequence: int64(round + 1), RowsSent: rows},
			}}); err != nil {
				return err
			}
		}
		<-ctx.Done()
		return nil
	})
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

	// Poll while the second snapshot streams. Once ready, the count must
	// never be observed below the full row set.
	stop := time.After(2 * time.Second)
	for {
		select {
		case <-stop:
			return
		default:
		}
		if n := c.Count(); n != rows {
			t.Fatalf("observed %d rows mid-resnapshot; a reader must never see a partial replica", n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRowKey_UsesDeclaredPrimaryKey covers the key-derivation defect: the
// client hardcoded "id", so a table keyed on anything else fell through to
// stringifying the whole row — and an UPDATE, having a different string,
// inserted a duplicate instead of replacing the original.
func TestRowKey_UsesDeclaredPrimaryKey(t *testing.T) {
	cols := []*v1.ColumnDefinition{
		{ColumnName: "tenant", DataType: "text", IsPrimaryKey: true, PrimaryKeyOrdinal: 1},
		{ColumnName: "flag", DataType: "text", IsPrimaryKey: true, PrimaryKeyOrdinal: 2},
		{ColumnName: "value", DataType: "text"},
	}
	ts := &testServer{}
	ts.setSyncFn(func(ctx context.Context, _ *v1.SyncStart, stream *testStream) error {
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_Handshake{
			Handshake: &v1.SyncHandshake{Mode: v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT, Columns: cols},
		}}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotBegin{
			SnapshotBegin: &v1.SnapshotBegin{SnapshotId: "s1", RowCount: 1},
		}}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotRow{
			SnapshotRow: &v1.SnapshotRow{Row: makeRow(t, map[string]any{"tenant": "acme", "flag": "x", "value": "off"})},
		}}); err != nil {
			return err
		}
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotEnd{
			SnapshotEnd: &v1.SnapshotEnd{Sequence: 1, RowsSent: 1},
		}}); err != nil {
			return err
		}
		// Update the same row: a non-key column changes.
		if err := sendJournal(stream, 2, "UPDATE",
			map[string]any{"tenant": "acme", "flag": "x", "value": "on"}, t); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	})
	addr := startTestServer(t, ts)

	c := New(ServerAddress(addr), Table("public", "feature_flag"), ClientID("t"))
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
		if row, ok := c.Get("acme\x00x"); ok && row["value"] == "on" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := c.Count(); n != 1 {
		t.Errorf("Count() = %d, want 1 — the update was keyed differently and duplicated the row", n)
	}
	row, ok := c.Get("acme\x00x")
	if !ok {
		t.Fatalf("row not found under its composite primary key; keys present: %v", keysOf(c))
	}
	if row["value"] != "on" {
		t.Errorf("value = %v, want \"on\" (the update did not replace the original)", row["value"])
	}
}

func keysOf(c *Client) []string {
	var out []string
	for k := range c.All() {
		out = append(out, fmt.Sprint(k))
	}
	return out
}
