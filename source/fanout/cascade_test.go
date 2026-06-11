package fanout_test

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
	"github.com/zourzouvillys/laredo/service/replication"
	srcfanout "github.com/zourzouvillys/laredo/source/fanout"
	"github.com/zourzouvillys/laredo/source/testsource"
	tgtfanout "github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// intCmp orders the integer-string positions the test source emits. The empty
// string (a fresh snapshot's reset position) sorts lowest.
func intCmp(a, b string) int {
	pa, ea := strconv.Atoi(a)
	pb, eb := strconv.Atoi(b)
	switch {
	case ea != nil && eb != nil:
		return 0
	case ea != nil:
		return -1
	case eb != nil:
		return 1
	case pa < pb:
		return -1
	case pa > pb:
		return 1
	default:
		return 0
	}
}

// startUpstream builds an upstream engine (testsource → fan-out target) serving
// the replication service over in-process HTTP, and returns its address and the
// source. One baseline row (alice) is loaded.
func startUpstream(t *testing.T) (string, *testsource.Source, laredo.TableIdentifier) {
	t.Helper()
	src := testsource.New()
	tbl := testutil.SampleTable()
	src.SetSchema(tbl, testutil.SampleColumns())
	src.AddRow(tbl, testutil.SampleRow(1, "alice"))

	ft := tgtfanout.New()
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", tbl, ft),
	)
	if len(errs) > 0 {
		t.Fatalf("upstream engine: %v", errs)
	}
	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("upstream start: %v", err)
	}
	if !eng.AwaitReady(5 * time.Second) {
		t.Fatal("upstream not ready")
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	path, handler := replicationv1connect.NewLaredoReplicationServiceHandler(replication.New(eng))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { _ = srv.Close() })

	return lis.Addr().String(), src, tbl
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestCascade_BaselineAndLive verifies a full cascade: an upstream fan-out
// consumed by a downstream engine via source/fanout into an in-memory target.
// The upstream baseline and a subsequent live change both propagate downstream.
func TestCascade_BaselineAndLive(t *testing.T) {
	addr, upstream, tbl := startUpstream(t)

	src := srcfanout.New(
		srcfanout.ServerAddress(addr),
		srcfanout.Table(tbl.Schema, tbl.Table),
		srcfanout.ClientID("downstream"),
		srcfanout.WithPositionComparator(intCmp),
	)
	mem := memory.NewIndexedTarget(memory.LookupFields("id"))
	eng, errs := laredo.NewEngine(
		laredo.WithSource("up", src),
		laredo.WithPipeline("up", tbl, mem),
	)
	if len(errs) > 0 {
		t.Fatalf("downstream engine: %v", errs)
	}
	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("downstream start: %v", err)
	}
	if !eng.AwaitReady(10 * time.Second) {
		t.Fatal("downstream not ready")
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	// Baseline propagated across the cascade.
	if got := mem.Count(); got != 1 {
		t.Fatalf("after baseline, downstream count = %d, want 1 (alice)", got)
	}

	// A live change on the upstream propagates: testsource → upstream fan-out →
	// source/fanout → downstream engine → in-memory target.
	upstream.EmitInsert(tbl, testutil.SampleRow(2, "bob"))
	if !waitFor(func() bool { return mem.Count() == 2 }, 5*time.Second) {
		t.Fatalf("live insert did not propagate; downstream count = %d, want 2", mem.Count())
	}

	names := map[string]bool{}
	for _, row := range mem.All() {
		names[row.GetString("name")] = true
	}
	if !names["alice"] || !names["bob"] {
		t.Fatalf("downstream names = %v, want both alice and bob", names)
	}
}
