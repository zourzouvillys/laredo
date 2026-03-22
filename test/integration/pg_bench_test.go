//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestPGBench_BaselineThroughput(t *testing.T) {
	// Measures baseline loading throughput from real PostgreSQL.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Insert 10K rows for baseline benchmark.
	const rowCount = 10000
	padding := strings.Repeat("x", 50)
	for i := range rowCount {
		_, err = conn.Exec(ctx,
			"INSERT INTO test_users (name, email) VALUES ($1, $2)",
			fmt.Sprintf("bench-%d", i), fmt.Sprintf("bench%d@%s", i, padding),
		)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	t.Logf("inserted %d rows for baseline benchmark", rowCount)

	target := memory.NewIndexedTarget()

	start := time.Now()
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
	if !eng.AwaitReady(60 * time.Second) {
		t.Fatal("not ready")
	}
	elapsed := time.Since(start)

	if target.Count() != rowCount {
		t.Errorf("expected %d rows, got %d", rowCount, target.Count())
	}

	rowsPerSec := float64(rowCount) / elapsed.Seconds()
	t.Logf("baseline: %d rows in %v (%.0f rows/sec)", rowCount, elapsed, rowsPerSec)

	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestPGBench_StreamingThroughput(t *testing.T) {
	// Measures streaming change throughput from real PostgreSQL.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 1) // 1 baseline row

	ctx := context.Background()
	target := memory.NewIndexedTarget()

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", pg.New(
			pg.Connection(pgc.ConnStr),
			pg.SlotName("bench_stream_slot"),
			pg.Publication(pg.PublicationConfig{Name: "bench_stream_pub", Create: true}),
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

	// Stream 1000 inserts.
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	const streamCount = 1000
	start := time.Now()
	for i := range streamCount {
		_, err = conn.Exec(ctx,
			"INSERT INTO test_users (name, email) VALUES ($1, $2)",
			fmt.Sprintf("stream-%d", i), fmt.Sprintf("s%d@test.com", i),
		)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	insertDone := time.Since(start)

	// Wait for all rows to arrive.
	expected := 1 + streamCount
	testutil.AssertEventually(t, 30*time.Second, func() bool {
		return target.Count() == expected
	}, fmt.Sprintf("expected %d rows", expected))
	elapsed := time.Since(start)

	changesPerSec := float64(streamCount) / elapsed.Seconds()
	t.Logf("streaming: %d changes in %v (%.0f changes/sec, inserts took %v)",
		streamCount, elapsed, changesPerSec, insertDone)

	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
