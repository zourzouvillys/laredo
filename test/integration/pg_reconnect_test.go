//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestPGSource_Reconnect(t *testing.T) {
	// Tests that the PG source auto-reconnects after the replication connection is killed.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 2)

	ctx := context.Background()
	target := memory.NewIndexedTarget(memory.LookupFields("name"))

	slotName := "laredo_reconnect_slot"
	src := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotModeOpt(pg.SlotStateful),
		pg.SlotName(slotName),
		pg.Publication(pg.PublicationConfig{Name: "laredo_reconnect_pub", Create: true}),
	)

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	if !eng.AwaitReady(30 * time.Second) {
		t.Fatal("engine not ready")
	}

	if target.Count() != 2 {
		t.Fatalf("expected 2 baseline rows, got %d", target.Count())
	}
	t.Logf("baseline complete: %d rows", target.Count())

	// Insert a row to verify streaming is working.
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "pre-kill", "pre@kill.com")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows before kill")
	t.Log("streaming verified: 3 rows")

	// Kill the WAL sender process (simulates connection loss).
	// Use pg_stat_activity to find the walsender backend by its application_name.
	var killed int
	qRows, err := conn.Query(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE backend_type = 'walsender'",
	)
	if err != nil {
		t.Fatalf("query pg_stat_activity: %v", err)
	}
	for qRows.Next() {
		var ok bool
		if err := qRows.Scan(&ok); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if ok {
			killed++
		}
	}
	qRows.Close()
	if killed == 0 {
		t.Fatal("no WAL sender processes found to kill")
	}
	t.Logf("killed %d WAL sender process(es)", killed)

	// Wait a moment for the disconnection to propagate.
	time.Sleep(500 * time.Millisecond)

	// Insert another row — should arrive after reconnect.
	_, err = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "post-kill", "post@kill.com")
	if err != nil {
		t.Fatalf("insert after kill: %v", err)
	}
	t.Log("inserted row after kill")

	// The engine should auto-reconnect and pick up the new row.
	testutil.AssertEventually(t, 30*time.Second, func() bool {
		return target.Count() == 4
	}, "expected 4 rows after reconnect")

	// Verify the post-kill row arrived.
	row, ok := target.Lookup("post-kill")
	if !ok {
		t.Fatal("post-kill row not found after reconnect")
	}
	if fmt.Sprintf("%v", row["email"]) != "post@kill.com" {
		t.Errorf("expected email=post@kill.com, got %v", row["email"])
	}

	// Insert yet another row to confirm streaming is stable.
	_, err = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "post-reconnect", "reconnected@test.com")
	if err != nil {
		t.Fatalf("insert after reconnect: %v", err)
	}

	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return target.Count() == 5
	}, "expected 5 rows after second post-kill insert")

	t.Logf("reconnect test passed: %d rows, streaming stable after WAL sender kill", target.Count())
}
