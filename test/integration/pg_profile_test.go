//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestProfile_BaselineLargeTable(t *testing.T) {
	// Profiles baseline loading of a large table.
	// Set LAREDO_PROFILE_ROWS to control row count (default: 100K).
	// Set to 1000000 for the full 1M profiling run.
	rowCount := 100_000
	if v := os.Getenv("LAREDO_PROFILE_ROWS"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &rowCount); err != nil {
			t.Fatalf("invalid LAREDO_PROFILE_ROWS: %v", err)
		}
	}

	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Bulk insert using COPY for speed.
	t.Logf("inserting %d rows...", rowCount)
	insertStart := time.Now()

	// Use batched inserts (1000 per batch) for reasonable speed.
	batch := 1000
	padding := strings.Repeat("x", 50)
	for i := 0; i < rowCount; i += batch {
		end := i + batch
		if end > rowCount {
			end = rowCount
		}
		var values []string
		for j := i; j < end; j++ {
			values = append(values, fmt.Sprintf("('%s', '%s%d@test.com')",
				fmt.Sprintf("profile-%d", j), padding, j))
		}
		_, err := conn.Exec(ctx,
			"INSERT INTO test_users (name, email) VALUES "+strings.Join(values, ","))
		if err != nil {
			t.Fatalf("batch insert at %d: %v", i, err)
		}
	}
	t.Logf("insert complete: %d rows in %v", rowCount, time.Since(insertStart))

	// Force GC and record baseline memory.
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// Profile the baseline load.
	target := memory.NewIndexedTarget(memory.LookupFields("name"))

	loadStart := time.Now()
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", pg.New(pg.Connection(pgc.ConnStr))),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !eng.AwaitReady(120 * time.Second) {
		t.Fatal("not ready within 120s")
	}
	loadDuration := time.Since(loadStart)

	if target.Count() != rowCount {
		t.Errorf("expected %d rows, got %d", rowCount, target.Count())
	}

	// Record memory after load.
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	heapUsed := memAfter.HeapInuse - memBefore.HeapInuse
	bytesPerRow := float64(heapUsed) / float64(rowCount)
	rowsPerSec := float64(rowCount) / loadDuration.Seconds()

	t.Logf("=== Profile Results ===")
	t.Logf("Rows:          %d", rowCount)
	t.Logf("Load time:     %v", loadDuration)
	t.Logf("Throughput:    %.0f rows/sec", rowsPerSec)
	t.Logf("Heap used:     %.1f MB", float64(heapUsed)/(1024*1024))
	t.Logf("Bytes/row:     %.0f", bytesPerRow)
	t.Logf("Total allocs:  %d", memAfter.TotalAlloc-memBefore.TotalAlloc)
	t.Logf("Alloc/row:     %.0f bytes", float64(memAfter.TotalAlloc-memBefore.TotalAlloc)/float64(rowCount))

	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestProfile_StreamingThroughput(t *testing.T) {
	// Profiles sustained streaming throughput.
	changeCount := 5000
	if v := os.Getenv("LAREDO_PROFILE_CHANGES"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &changeCount); err != nil {
			t.Fatalf("invalid LAREDO_PROFILE_CHANGES: %v", err)
		}
	}

	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 1)

	ctx := context.Background()
	target := memory.NewIndexedTarget()

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", pg.New(
			pg.Connection(pgc.ConnStr),
			pg.SlotName("profile_stream_slot"),
			pg.Publication(pg.PublicationConfig{Name: "profile_stream_pub", Create: true}),
		)),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !eng.AwaitReady(30 * time.Second) {
		t.Fatal("not ready")
	}

	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Measure sustained streaming.
	t.Logf("streaming %d changes...", changeCount)
	streamStart := time.Now()

	batch := 100
	for i := 0; i < changeCount; i += batch {
		end := i + batch
		if end > changeCount {
			end = changeCount
		}
		var values []string
		for j := i; j < end; j++ {
			values = append(values, fmt.Sprintf("('stream-%d', 's%d@t.com')", j, j))
		}
		_, err := conn.Exec(ctx,
			"INSERT INTO test_users (name, email) VALUES "+strings.Join(values, ","))
		if err != nil {
			t.Fatalf("batch insert at %d: %v", i, err)
		}
	}
	insertDone := time.Since(streamStart)

	expected := 1 + changeCount
	testutil.AssertEventually(t, 60*time.Second, func() bool {
		return target.Count() >= expected
	}, fmt.Sprintf("expected %d rows", expected))
	totalDuration := time.Since(streamStart)

	changesPerSec := float64(changeCount) / totalDuration.Seconds()

	t.Logf("=== Streaming Profile ===")
	t.Logf("Changes:       %d", changeCount)
	t.Logf("Insert time:   %v", insertDone)
	t.Logf("Total time:    %v (includes replication lag)", totalDuration)
	t.Logf("Throughput:    %.0f changes/sec", changesPerSec)

	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
