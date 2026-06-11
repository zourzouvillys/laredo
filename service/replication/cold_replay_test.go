package replication

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func ptr(s string) *string { return &s }

// writeColdArchive writes a manifest into dir describing: a base snapshot at
// position "1" (alice) and a diff (1→2) inserting bob; head = "2". This is the
// history a client that fell behind the hot journal needs.
func writeColdArchive(t *testing.T, dir string) {
	t.Helper()
	dest := local.New(dir)
	f := jsonl.New()
	ctx := context.Background()
	put := func(art snapshotter.Artifact, payload []byte) {
		key := snapshotter.ArtifactObjectKey("", art, f.Extension())
		if _, _, err := dest.Put(ctx, key, bytes.NewReader(payload)); err != nil {
			t.Fatalf("put artifact: %v", err)
		}
	}

	snapArt := snapshotter.Artifact{Kind: snapshotter.KindSnapshot, Epoch: 1, ToPosition: "1", RowCount: 1, Formats: map[string]snapshotter.FormatRef{"jsonl": {}}}
	var sb bytes.Buffer
	if err := f.WriteSnapshot(&sb, []laredo.Row{{"id": 1, "name": "alice"}}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	put(snapArt, sb.Bytes())

	diffArt := snapshotter.Artifact{Kind: snapshotter.KindDiff, Epoch: 1, FromPosition: ptr("1"), ToPosition: "2", ChangeCount: 1, Formats: map[string]snapshotter.FormatRef{"jsonl": {}}}
	var db bytes.Buffer
	if err := f.WriteDiff(&db, []snapshotter.Change{{Action: laredo.ActionInsert, Key: "2", New: laredo.Row{"id": 2, "name": "bob"}}}); err != nil {
		t.Fatalf("write diff: %v", err)
	}
	put(diffArt, db.Bytes())

	m := snapshotter.Manifest{
		ManifestVersion: snapshotter.ManifestVersion,
		Table:           "public.test_table",
		Epoch:           1,
		HeadPosition:    "2",
		Artifacts:       []snapshotter.Artifact{snapArt, diffArt},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if _, _, err := dest.Put(ctx, snapshotter.ManifestObjectKey(""), bytes.NewReader(data)); err != nil {
		t.Fatalf("put manifest: %v", err)
	}
}

// startColdService builds an engine (testsource → fan-out with a tiny journal so
// old entries prune) wired to a replication service that can read the archive in
// dir, and serves it. One baseline row (alice) is loaded.
func startColdService(t *testing.T, dir string, journalMax int) (replicationv1connect.LaredoReplicationServiceClient, *fanout.Target, *testsource.Source, laredo.TableIdentifier) {
	t.Helper()
	reader, err := snapshotter.NewReader(local.New(dir), "", jsonl.New())
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	src := testsource.New()
	tbl := testutil.SampleTable()
	src.SetSchema(tbl, testutil.SampleColumns())
	src.AddRow(tbl, testutil.SampleRow(1, "alice"))

	ft := fanout.New(fanout.JournalMaxEntries(journalMax))
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

	svc := New(eng, WithArchive("public", "test_table", reader))
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

	client := replicationv1connect.NewLaredoReplicationServiceClient(http.DefaultClient, "http://"+listener.Addr().String())
	return client, ft, src, tbl
}

// coldResult collects what a Sync stream delivered, up to a stopping condition.
type coldResult struct {
	mode         v1.SyncMode
	snapshot     []string // snapshot row names, in order
	journal      []string // journal-entry new-value names, in order
	sawSnapEnd   bool
	sawSnapBegin bool
}

func drainColdSync(t *testing.T, stream *connect.ServerStreamForClient[v1.SyncResponse], wantJournal int) coldResult {
	t.Helper()
	var r coldResult
	for stream.Receive() {
		switch m := stream.Msg().GetMessage().(type) {
		case *v1.SyncResponse_Handshake:
			r.mode = m.Handshake.GetMode()
		case *v1.SyncResponse_SnapshotBegin:
			r.sawSnapBegin = true
		case *v1.SyncResponse_SnapshotRow:
			r.snapshot = append(r.snapshot, laredo.Row(m.SnapshotRow.GetRow().AsMap()).GetString("name"))
		case *v1.SyncResponse_SnapshotEnd:
			r.sawSnapEnd = true
		case *v1.SyncResponse_JournalEntry:
			r.journal = append(r.journal, laredo.Row(m.JournalEntry.GetNewValues().AsMap()).GetString("name"))
		}
		if len(r.journal) >= wantJournal {
			break
		}
	}
	return r
}

// TestSync_ColdTierReplay_SnapshotBase: a client whose position predates the hot
// journal, with no diff starting at its position, is served the archive base
// snapshot + diffs, then handed off gaplessly to the hot journal.
func TestSync_ColdTierReplay_SnapshotBase(t *testing.T) {
	dir := t.TempDir()
	writeColdArchive(t, dir)
	client, ft, src, tbl := startColdService(t, dir, 2) // journal keeps 2 → pos "1" prunes

	// baseline alice@1; emit bob@2, carol@3 → journal (max 2) retains seq 2,3.
	src.EmitInsert(tbl, testutil.SampleRow(2, "bob"))
	src.EmitInsert(tbl, testutil.SampleRow(3, "carol"))
	waitJournalSeq(t, ft, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Sync(ctx, connect.NewRequest(&v1.SyncRequest{
		Schema:                  tbl.Schema,
		Table:                   tbl.Table,
		ClientId:                "stale-snapbase",
		LastKnownSourcePosition: "0", // predates the archive's first diff boundary → snapshot-base
	}))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	r := drainColdSync(t, stream, 2)
	if r.mode != v1.SyncMode_SYNC_MODE_REPLAY_ARCHIVE {
		t.Fatalf("mode = %v, want REPLAY_ARCHIVE", r.mode)
	}
	if len(r.snapshot) != 1 || r.snapshot[0] != "alice" {
		t.Fatalf("snapshot rows = %v, want [alice] (from cold archive)", r.snapshot)
	}
	// bob from the cold diff, carol from the hot-journal handoff — gapless, in order.
	if len(r.journal) != 2 || r.journal[0] != "bob" || r.journal[1] != "carol" {
		t.Fatalf("journal entries = %v, want [bob carol] (cold diff then hot handoff)", r.journal)
	}
}

// TestSync_ColdTierReplay_DiffOnly: a client whose position aligns exactly to a
// diff boundary gets diff-only (no base snapshot re-sent), then the hot handoff.
func TestSync_ColdTierReplay_DiffOnly(t *testing.T) {
	dir := t.TempDir()
	writeColdArchive(t, dir)
	client, ft, src, tbl := startColdService(t, dir, 2)

	src.EmitInsert(tbl, testutil.SampleRow(2, "bob"))
	src.EmitInsert(tbl, testutil.SampleRow(3, "carol"))
	waitJournalSeq(t, ft, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Sync(ctx, connect.NewRequest(&v1.SyncRequest{
		Schema:                  tbl.Schema,
		Table:                   tbl.Table,
		ClientId:                "stale-diffonly",
		LastKnownSourcePosition: "1", // exactly the diff's from-position → diff-only
	}))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	r := drainColdSync(t, stream, 2)
	if r.mode != v1.SyncMode_SYNC_MODE_REPLAY_ARCHIVE {
		t.Fatalf("mode = %v, want REPLAY_ARCHIVE", r.mode)
	}
	if r.sawSnapBegin {
		t.Fatal("diff-only replay must not send a base snapshot")
	}
	if len(r.journal) != 2 || r.journal[0] != "bob" || r.journal[1] != "carol" {
		t.Fatalf("journal entries = %v, want [bob carol]", r.journal)
	}
}

// TestSync_ColdTierReplay_GapFallsBackToFullSnapshot: when the hot journal has
// pruned past the archive head (a gap cold replay cannot bridge), the server
// falls back to a full live snapshot.
func TestSync_ColdTierReplay_GapFallsBackToFullSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeColdArchive(t, dir)                            // archive head = "2"
	client, ft, src, tbl := startColdService(t, dir, 1) // journal keeps only the newest entry

	// Advance well past the archive head so the journal's oldest position > "2".
	src.EmitInsert(tbl, testutil.SampleRow(2, "bob"))
	src.EmitInsert(tbl, testutil.SampleRow(3, "carol"))
	src.EmitInsert(tbl, testutil.SampleRow(4, "dave"))
	waitJournalSeq(t, ft, 4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Sync(ctx, connect.NewRequest(&v1.SyncRequest{
		Schema:                  tbl.Schema,
		Table:                   tbl.Table,
		ClientId:                "gap",
		LastKnownSourcePosition: "0",
	}))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	var mode v1.SyncMode
	var sawSnapshot bool
	for stream.Receive() {
		switch m := stream.Msg().GetMessage().(type) {
		case *v1.SyncResponse_Handshake:
			mode = m.Handshake.GetMode()
		case *v1.SyncResponse_SnapshotBegin:
			sawSnapshot = true
		}
		if sawSnapshot {
			break
		}
	}
	if mode != v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT {
		t.Fatalf("mode = %v, want FULL_SNAPSHOT (gap must fall back, not cold-replay)", mode)
	}
}
