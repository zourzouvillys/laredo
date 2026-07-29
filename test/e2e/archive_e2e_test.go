//go:build e2e

// Package e2e runs full-stack tests: a laredo engine assembled from HOCON config,
// fronted by the real Query gRPC service, exercised over the wire by a real
// client. These tests need no external services — the archive source replays a
// snapshotter archive from a temp dir, so the whole flow (config → engine →
// archive source → target → gRPC query) runs with no database. Build-tagged `e2e`
// (run: `go test -tags=e2e ./test/e2e/...`).
package e2e

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/config"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
	"github.com/zourzouvillys/laredo/service"
	"github.com/zourzouvillys/laredo/service/query"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
)

var eventCols = []laredo.ColumnDefinition{
	{Name: "id", Type: "int8", PrimaryKey: true, PrimaryKeyOrdinal: 1, OrdinalPosition: 1},
	{Name: "name", Type: "text", Nullable: true, OrdinalPosition: 2},
}

// writeArchive writes a one-shot base-snapshot archive for table under prefix at
// dir. Re-calling it with a higher position is a wholesale replacement.
func writeArchive(t *testing.T, dir, prefix, table, pos string, rows []laredo.Row) {
	t.Helper()
	err := snapshotter.WriteBaseSnapshot(context.Background(),
		[]snapshotter.Destination{local.New(dir)}, prefix,
		[]snapshotter.Format{jsonl.New()}, table, pos, rows, eventCols, time.Now())
	if err != nil {
		t.Fatalf("write archive %s: %v", prefix, err)
	}
}

// writeConf writes a HOCON config file and returns its path.
func writeConf(t *testing.T, hocon string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "laredo.conf")
	if err := os.WriteFile(path, []byte(hocon), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	return path
}

// testServer is an in-process engine + Query gRPC service assembled from config.
type testServer struct {
	client laredov1connect.LaredoQueryServiceClient
}

// startServer loads confPath the way laredo-server does (config → engine → query
// service) and fronts it with a real gRPC listener on a random port.
func startServer(t *testing.T, confPath string) *testServer {
	t.Helper()
	cfg, err := config.Load(confPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	opts, err := cfg.ToEngineOptions()
	if err != nil {
		t.Fatalf("engine options: %v", err)
	}
	eng, errs := laredo.NewEngine(opts...)
	if len(errs) > 0 {
		t.Fatalf("new engine: %v", errs)
	}
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	srv := service.New(service.WithAddress("127.0.0.1:0"), service.EnableQuery(query.New(eng)))
	// Start blocks on Serve, so run it in the background and wait for the listener
	// to bind (Addr becomes non-empty) before building a client against it.
	go func() { _ = srv.Start() }()
	var addr string
	for range 200 {
		if addr = srv.Addr(); addr != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("gRPC server did not bind within 1s")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
		_ = eng.Stop(ctx)
	})
	return &testServer{
		client: laredov1connect.NewLaredoQueryServiceClient(http.DefaultClient, "http://"+addr),
	}
}

func (ts *testServer) listRows(t *testing.T, schema, table string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := ts.client.ListRows(ctx, connect.NewRequest(&v1.ListRowsRequest{
		Schema: schema, Table: table, PageSize: 100,
	}))
	if err != nil {
		t.Fatalf("ListRows(%s.%s): %v", schema, table, err)
	}
	out := make([]map[string]any, 0, len(resp.Msg.GetRows()))
	for _, r := range resp.Msg.GetRows() {
		out = append(out, r.AsMap())
	}
	return out
}

// waitRows polls ListRows until the row count equals want (baseline is async), or
// fails after the timeout. Returns the rows.
func (ts *testServer) waitRows(t *testing.T, schema, table string, want int, timeout time.Duration) []map[string]any {
	t.Helper()
	return ts.waitUntil(t, schema, table, func(rows []map[string]any) bool {
		return len(rows) == want
	}, timeout)
}

// waitUntil polls ListRows until pred holds, or fails after the timeout.
func (ts *testServer) waitUntil(t *testing.T, schema, table string, pred func([]map[string]any) bool, timeout time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var rows []map[string]any
	for time.Now().Before(deadline) {
		rows = ts.listRows(t, schema, table)
		if pred(rows) {
			return rows
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s.%s: predicate not satisfied after %s (last: %v)", schema, table, timeout, names(rows))
	return nil
}

func names(rows []map[string]any) map[string]bool {
	set := make(map[string]bool, len(rows))
	for _, r := range rows {
		if n, ok := r["name"].(string); ok {
			set[n] = true
		}
	}
	return set
}

func archiveConf(dir string, follow bool) string {
	f := "false"
	if follow {
		f = "true"
	}
	return `
sources {
  seed {
    type = archive
    store = local
    store_config { path = "` + dir + `" }
    format = jsonl
    key_prefix = "public.events/"
    key_fields = [id]
    follow = ` + f + `
    poll_interval = 20ms
  }
}
tables = [
  { source = seed, schema = public, table = events, targets = [ { type = indexed-memory, lookup_fields = [name] } ] }
]
`
}

// TestSmoke_ArchiveSource is the smoke test: an engine booted from an archive
// config with no database serves the seeded rows over the Query gRPC.
func TestSmoke_ArchiveSource(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "public.events/", "public.events", "0/1",
		[]laredo.Row{{"id": 1, "name": "alice"}, {"id": 2, "name": "bob"}, {"id": 3, "name": "carol"}})

	ts := startServer(t, writeConf(t, archiveConf(dir, false)))
	rows := ts.waitRows(t, "public", "events", 3, 5*time.Second)

	got := names(rows)
	for _, want := range []string{"alice", "bob", "carol"} {
		if !got[want] {
			t.Errorf("missing row %q; got %v", want, got)
		}
	}
}

// TestE2E_ArchiveFollowReplacement verifies a follow source, running through the
// full server, re-baselines when the archive is wholesale-replaced on disk — the
// replacement's new rows become queryable with no restart.
//
// Note: laredo's re-baseline upserts rather than resets, so rows present only in
// the old archive persist in a memory target. The robust assertion is that the
// new archive's rows appear; we do not assert removal of the old ones.
func TestE2E_ArchiveFollowReplacement(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "public.events/", "public.events", "0/1",
		[]laredo.Row{{"id": 1, "name": "alice"}, {"id": 2, "name": "bob"}})

	ts := startServer(t, writeConf(t, archiveConf(dir, true)))
	base := ts.waitRows(t, "public", "events", 2, 5*time.Second) // initial baseline
	if !names(base)["alice"] {
		t.Fatalf("baseline missing alice: %v", names(base))
	}

	// Replace the archive wholesale: a higher head position with rows that did not
	// exist before, so the follower must re-baseline to serve them.
	writeArchive(t, dir, "public.events/", "public.events", "5/0",
		[]laredo.Row{{"id": 3, "name": "carol"}, {"id": 4, "name": "dave"}})

	rows := ts.waitUntil(t, "public", "events", func(rs []map[string]any) bool {
		n := names(rs)
		return n["carol"] && n["dave"] // the replacement's rows are now served
	}, 5*time.Second)
	t.Logf("after replacement: %v", names(rows))
}

// TestE2E_ArchiveGroup verifies a `group = true` block serves multiple tables
// through the full server, each from its own derived prefix.
func TestE2E_ArchiveGroup(t *testing.T) {
	root := t.TempDir()
	writeArchive(t, root, "public.events/", "public.events", "0/1",
		[]laredo.Row{{"id": 1, "name": "alice"}})
	writeArchive(t, root, "public.users/", "public.users", "0/1",
		[]laredo.Row{{"id": 10, "name": "dave"}, {"id": 11, "name": "erin"}})

	conf := `
sources {
  seed {
    type = archive
    group = true
    store = local
    store_config { path = "` + root + `" }
    format = jsonl
  }
}
tables = [
  { source = seed, schema = public, table = events, targets = [ { type = indexed-memory } ] }
  { source = seed, schema = public, table = users,  targets = [ { type = indexed-memory } ] }
]
`
	ts := startServer(t, writeConf(t, conf))

	events := ts.waitRows(t, "public", "events", 1, 5*time.Second)
	if !names(events)["alice"] {
		t.Errorf("events table: expected alice, got %v", names(events))
	}
	users := ts.waitRows(t, "public", "users", 2, 5*time.Second)
	if u := names(users); !u["dave"] || !u["erin"] {
		t.Errorf("users table: expected dave+erin, got %v", u)
	}
}
