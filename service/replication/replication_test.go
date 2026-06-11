package replication

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// startReplService builds a real engine (testsource → fan-out target) and serves
// the replication service over HTTP, returning a client, the fan-out target, and
// the source. Two baseline rows are loaded; callers emit changes to advance the
// journal (positions 2, 3, ... — the baseline position is 1).
func startReplService(t *testing.T) (replicationv1connect.LaredoReplicationServiceClient, *fanout.Target, *testsource.Source) {
	t.Helper()

	src := testsource.New()
	tbl := testutil.SampleTable()
	src.SetSchema(tbl, testutil.SampleColumns())
	src.AddRow(tbl, testutil.SampleRow(1, "alice"))
	src.AddRow(tbl, testutil.SampleRow(2, "bob"))

	ft := fanout.New()
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", tbl, ft),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !eng.AwaitReady(5 * time.Second) {
		t.Fatal("engine not ready")
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	path, handler := replicationv1connect.NewLaredoReplicationServiceHandler(New(eng))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	client := replicationv1connect.NewLaredoReplicationServiceClient(http.DefaultClient, "http://"+listener.Addr().String())
	return client, ft, src
}

// waitJournalSeq blocks until the fan-out journal reaches at least seq.
func waitJournalSeq(t *testing.T, ft *fanout.Target, seq int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ft.JournalSequence() >= seq {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("journal did not reach seq %d (got %d)", seq, ft.JournalSequence())
}

func TestSync_ResumeBySourcePosition(t *testing.T) {
	client, ft, src := startReplService(t)
	tbl := testutil.SampleTable()

	// Baseline: seq 1,2 (position 1). Emit two changes: positions 2, 3.
	src.EmitInsert(tbl, testutil.SampleRow(3, "carol")) // position 2, seq 3
	src.EmitUpdate(tbl, testutil.SampleRow(1, "alice2"), testutil.SampleRow(1, "alice"))
	waitJournalSeq(t, ft, 4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Resume as if we already applied up to source position "2" (the insert).
	stream, err := client.Sync(ctx, connect.NewRequest(&v1.SyncRequest{
		Schema:                  tbl.Schema,
		Table:                   tbl.Table,
		ClientId:                "resume-by-pos",
		LastKnownSourcePosition: "2",
	}))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	var mode v1.SyncMode
	var sawSnapshot bool
	var deltaPos string
	for stream.Receive() {
		switch m := stream.Msg().GetMessage().(type) {
		case *v1.SyncResponse_Handshake:
			mode = m.Handshake.GetMode()
		case *v1.SyncResponse_SnapshotBegin:
			sawSnapshot = true
		case *v1.SyncResponse_JournalEntry:
			deltaPos = m.JournalEntry.GetSourcePosition()
		}
		if deltaPos != "" {
			break
		}
	}

	if mode != v1.SyncMode_SYNC_MODE_DELTA {
		t.Fatalf("mode = %v, want DELTA", mode)
	}
	if sawSnapshot {
		t.Fatal("resume by position must not send a full snapshot")
	}
	if deltaPos != "3" {
		t.Fatalf("delta resumed at source_position %q, want \"3\"", deltaPos)
	}
}

func TestSync_TooOldPositionFallsBackToSnapshot(t *testing.T) {
	client, ft, src := startReplService(t)
	tbl := testutil.SampleTable()
	src.EmitInsert(tbl, testutil.SampleRow(3, "carol"))
	waitJournalSeq(t, ft, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Position "0" predates the oldest retained entry (baseline position is 1).
	stream, err := client.Sync(ctx, connect.NewRequest(&v1.SyncRequest{
		Schema:                  tbl.Schema,
		Table:                   tbl.Table,
		ClientId:                "too-old",
		LastKnownSourcePosition: "0",
	}))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	var mode v1.SyncMode
	var sawSnapshotBegin bool
	for stream.Receive() {
		switch m := stream.Msg().GetMessage().(type) {
		case *v1.SyncResponse_Handshake:
			mode = m.Handshake.GetMode()
		case *v1.SyncResponse_SnapshotBegin:
			sawSnapshotBegin = true
		case *v1.SyncResponse_SnapshotEnd:
			// Snapshot fully sent — stop.
		}
		if sawSnapshotBegin {
			break
		}
	}

	if mode != v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT {
		t.Fatalf("mode = %v, want FULL_SNAPSHOT", mode)
	}
	if !sawSnapshotBegin {
		t.Fatal("expected a full snapshot for a too-old position")
	}
}

func TestSync_DrainSendsGoAway(t *testing.T) {
	client, ft, _ := startReplService(t)
	tbl := testutil.SampleTable()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := client.Sync(ctx, connect.NewRequest(&v1.SyncRequest{
		Schema:   tbl.Schema,
		Table:    tbl.Table,
		ClientId: "drain-me",
	}))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	goAway := make(chan string, 1)
	go func() {
		for stream.Receive() {
			if ga := stream.Msg().GetGoAway(); ga != nil {
				goAway <- ga.GetReason()
				return
			}
		}
	}()

	// Let the client reach the live phase, then drain.
	time.Sleep(100 * time.Millisecond)
	ft.Drain("admin", time.Time{})

	select {
	case reason := <-goAway:
		if reason != "admin" {
			t.Fatalf("GoAway reason = %q, want \"admin\"", reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive GoAway after drain")
	}
}

// TestSync_SubscriptionFilter verifies that a per-subscription filter is applied
// uniformly to the snapshot and the live stream: a subscriber sees only matching
// rows, and excluded rows never cross the wire.
func TestSync_SubscriptionFilter(t *testing.T) {
	client, ft, src := startReplService(t)
	tbl := testutil.SampleTable()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Filter on name prefix "a": baseline alice matches, bob does not.
	stream, err := client.Sync(ctx, connect.NewRequest(&v1.SyncRequest{
		Schema:   tbl.Schema,
		Table:    tbl.Table,
		ClientId: "filtered",
		Filters:  []*v1.FieldPredicate{prefixPred("name", "a")},
	}))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	var snapshotRowCount int64
	var snapshotNames []string
	var changesEmitted bool
	var firstChangeName string

	for stream.Receive() {
		switch m := stream.Msg().GetMessage().(type) {
		case *v1.SyncResponse_SnapshotBegin:
			snapshotRowCount = m.SnapshotBegin.GetRowCount()
		case *v1.SyncResponse_SnapshotRow:
			row := laredo.Row(m.SnapshotRow.GetRow().AsMap())
			snapshotNames = append(snapshotNames, row.GetString("name"))
		case *v1.SyncResponse_SnapshotEnd:
			// Snapshot taken. Emit anna (matches), plus carol and a bob update
			// (both excluded). carol is journaled before anna, so if filtering
			// were broken it would arrive first — receiving anna first proves
			// carol was filtered out of the live stream.
			src.EmitInsert(tbl, testutil.SampleRow(3, "carol"))
			src.EmitInsert(tbl, testutil.SampleRow(4, "anna"))
			src.EmitUpdate(tbl, testutil.SampleRow(2, "bart"), testutil.SampleRow(2, "bob"))
			waitJournalSeq(t, ft, 5)
			changesEmitted = true
		case *v1.SyncResponse_JournalEntry:
			if !changesEmitted {
				continue
			}
			row := laredo.Row(m.JournalEntry.GetNewValues().AsMap())
			firstChangeName = row.GetString("name")
		}
		if firstChangeName != "" {
			break
		}
	}

	if snapshotRowCount != 1 {
		t.Fatalf("SnapshotBegin.RowCount = %d, want 1 (only the filtered row)", snapshotRowCount)
	}
	if len(snapshotNames) != 1 || snapshotNames[0] != "alice" {
		t.Fatalf("snapshot rows = %v, want [alice]", snapshotNames)
	}
	if firstChangeName != "anna" {
		t.Fatalf("first delivered change = %q, want \"anna\" (carol and bob must be filtered out)", firstChangeName)
	}
}
