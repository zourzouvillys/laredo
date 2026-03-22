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

func TestPGSource_SlotInvalidation_ResetAndRebaseline(t *testing.T) {
	// Tests the recovery flow for slot invalidation: stop engine, generate
	// WAL, reset source (drop slot), restart with fresh baseline.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 2)

	ctx := context.Background()
	slotName := "laredo_reset_test_slot"
	pubName := "laredo_reset_test_pub"

	// Run 1: baseline + verify.
	target1 := memory.NewIndexedTarget()
	eng1, errs := laredo.NewEngine(
		laredo.WithSource("pg", pg.New(
			pg.Connection(pgc.ConnStr),
			pg.SlotModeOpt(pg.SlotStateful),
			pg.SlotName(slotName),
			pg.Publication(pg.PublicationConfig{Name: pubName, Create: true}),
		)),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target1),
	)
	if len(errs) > 0 {
		t.Fatalf("engine1 errors: %v", errs)
	}

	if err := eng1.Start(ctx); err != nil {
		t.Fatalf("start1: %v", err)
	}
	if !eng1.AwaitReady(30 * time.Second) {
		t.Fatal("engine1 not ready")
	}
	if target1.Count() != 2 {
		t.Fatalf("expected 2 baseline rows, got %d", target1.Count())
	}

	// Stop engine 1 — slot is now idle.
	if err := eng1.Stop(ctx); err != nil {
		t.Fatalf("stop1: %v", err)
	}
	t.Log("run 1 complete: 2 rows, stopped")

	// Insert more data while engine is offline.
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	padding := strings.Repeat("x", 100)
	for i := range 500 {
		_, err = conn.Exec(ctx,
			"INSERT INTO test_users (name, email) VALUES ($1, $2)",
			fmt.Sprintf("offline-%d", i), padding,
		)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	t.Log("inserted 500 rows while offline")

	// Simulate operator recovery: reset the source (drop slot, keep publication).
	resetSrc := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotModeOpt(pg.SlotStateful),
		pg.SlotName(slotName),
		pg.Publication(pg.PublicationConfig{Name: pubName}),
	)
	// Init to establish connections before reset.
	if _, err := resetSrc.Init(ctx, laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{laredo.Table("public", "test_users")},
	}); err != nil {
		t.Fatalf("init resetSrc: %v", err)
	}
	if err := resetSrc.ResetSource(ctx, false); err != nil {
		t.Fatalf("ResetSource: %v", err)
	}
	if err := resetSrc.Close(ctx); err != nil {
		t.Logf("close resetSrc: %v", err)
	}
	t.Log("slot reset complete")

	// Run 2: fresh engine should create a new slot and baseline all 502 rows.
	target2 := memory.NewIndexedTarget()
	eng2, errs := laredo.NewEngine(
		laredo.WithSource("pg", pg.New(
			pg.Connection(pgc.ConnStr),
			pg.SlotModeOpt(pg.SlotStateful),
			pg.SlotName(slotName),
			pg.Publication(pg.PublicationConfig{Name: pubName, Create: true}),
		)),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target2),
	)
	if len(errs) > 0 {
		t.Fatalf("engine2 errors: %v", errs)
	}

	if err := eng2.Start(ctx); err != nil {
		t.Fatalf("start2: %v", err)
	}
	if !eng2.AwaitReady(30 * time.Second) {
		t.Fatal("engine2 not ready")
	}

	if target2.Count() != 502 {
		t.Errorf("expected 502 rows after reset + re-baseline, got %d", target2.Count())
	}

	if err := eng2.Stop(ctx); err != nil {
		t.Fatalf("stop2: %v", err)
	}

	t.Logf("recovery test passed: %d rows after slot reset and re-baseline", target2.Count())
}
