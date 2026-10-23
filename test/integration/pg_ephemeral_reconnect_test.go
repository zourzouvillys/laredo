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

// TestPGSource_EphemeralReconnect is the ephemeral-slot counterpart of
// TestPGSource_Reconnect. A temporary slot is dropped by PostgreSQL when the
// connection that owns it ends, so after a disconnect there is nothing to
// resume: the source has to open a new replication connection, create a new
// slot, and have the engine re-baseline from that slot.
//
// The DELETE after the kill is what separates "streaming again" from "stuck
// re-baselining". A re-baseline upserts every row it reads and never removes
// one, so a source that keeps failing to stream — and keeps re-baselining —
// still picks up the post-kill INSERT. Only a live stream carries the DELETE.
func TestPGSource_EphemeralReconnect(t *testing.T) {
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 2)

	ctx := context.Background()
	target := memory.NewIndexedTarget(memory.LookupFields("name"))
	obs := &testutil.TestObserver{}

	slotName := "laredo_ephemeral_reconnect_slot"
	src := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotModeOpt(pg.SlotEphemeral),
		pg.SlotName(slotName),
		pg.Publication(pg.PublicationConfig{Name: "laredo_ephemeral_reconnect_pub", Create: true}),
	)

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
		laredo.WithObserver(obs),
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

	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "pre-kill", "pre@kill.com"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows before kill")

	// Kill the WAL sender. PostgreSQL drops the temporary slot with it.
	var killed int
	if err := conn.QueryRow(ctx,
		"SELECT count(*) FILTER (WHERE pg_terminate_backend(pid)) FROM pg_stat_activity WHERE backend_type = 'walsender'",
	).Scan(&killed); err != nil {
		t.Fatalf("terminate walsender: %v", err)
	}
	if killed == 0 {
		t.Fatal("no WAL sender processes found to kill")
	}

	if _, err := conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "post-kill", "post@kill.com"); err != nil {
		t.Fatalf("insert after kill: %v", err)
	}
	testutil.AssertEventually(t, 30*time.Second, func() bool {
		_, ok := target.Lookup("post-kill")
		return ok
	}, "expected post-kill row after reconnect")

	// Only a live stream can deliver this.
	if _, err := conn.Exec(ctx, "DELETE FROM test_users WHERE name = $1", "pre-kill"); err != nil {
		t.Fatalf("delete after reconnect: %v", err)
	}
	testutil.AssertEventually(t, 15*time.Second, func() bool {
		_, ok := target.Lookup("pre-kill")
		return !ok
	}, "expected the DELETE to reach the target — the source is re-baselining instead of streaming")

	// One disconnect, one re-baseline — not a loop. Wait long enough for a
	// looping source to have gone round several times before counting.
	time.Sleep(3 * time.Second)
	if n := obs.EventCount("ReBaselineTriggered"); n != 1 {
		t.Errorf("expected exactly 1 re-baseline after one disconnect, got %d", n)
	}

	// The replacement slot exists and is the one being streamed from.
	var temporary, active bool
	if err := conn.QueryRow(ctx,
		"SELECT temporary, active FROM pg_replication_slots WHERE slot_name = $1", slotName,
	).Scan(&temporary, &active); err != nil {
		t.Fatalf("replacement slot %q not found: %v", slotName, err)
	}
	if !temporary || !active {
		t.Errorf("replacement slot: temporary=%v active=%v, want both true", temporary, active)
	}

	// The disconnect reason names what actually failed, not only the sentinel.
	disconnects := obs.EventsByType("SourceDisconnected")
	if len(disconnects) == 0 {
		t.Fatal("expected a SourceDisconnected event")
	}
	reason, _ := disconnects[0].Data["reason"].(string)
	t.Logf("disconnect reason: %s", reason)
	if !strings.Contains(reason, laredo.ErrReBaselineRequired.Error()) || strings.HasSuffix(reason, laredo.ErrReBaselineRequired.Error()) {
		t.Errorf("disconnect reason should carry the underlying error after the sentinel, got %q", reason)
	}
}

// TestPGSource_EphemeralReconnect_queryConnectionToo kills every connection the
// source holds — the query connection as well as the walsender — as a network
// partition or a server restart would. The re-baseline runs on the query
// connection, so recovery has to reopen that one too.
func TestPGSource_EphemeralReconnect_queryConnectionToo(t *testing.T) {
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 2)

	ctx := context.Background()
	target := memory.NewIndexedTarget(memory.LookupFields("name"))
	obs := &testutil.TestObserver{}

	src := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotModeOpt(pg.SlotEphemeral),
		pg.SlotName("laredo_ephemeral_both_slot"),
		pg.Publication(pg.PublicationConfig{Name: "laredo_ephemeral_both_pub", Create: true}),
	)
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
		laredo.WithObserver(obs),
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

	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	var killed int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE pg_terminate_backend(pid))
		FROM pg_stat_activity
		WHERE datname = current_database() AND pid <> pg_backend_pid()
		  AND backend_type IN ('client backend', 'walsender')`,
	).Scan(&killed); err != nil {
		t.Fatalf("terminate backends: %v", err)
	}
	if killed < 2 {
		t.Fatalf("expected to kill the query connection and the walsender, killed %d", killed)
	}

	if _, err := conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "post-kill", "post@kill.com"); err != nil {
		t.Fatalf("insert after kill: %v", err)
	}
	testutil.AssertEventually(t, 30*time.Second, func() bool {
		_, ok := target.Lookup("post-kill")
		return ok
	}, "expected post-kill row after both connections were replaced")

	if _, err := conn.Exec(ctx, "DELETE FROM test_users WHERE name = $1", "post-kill"); err != nil {
		t.Fatalf("delete after reconnect: %v", err)
	}
	testutil.AssertEventually(t, 15*time.Second, func() bool {
		_, ok := target.Lookup("post-kill")
		return !ok
	}, "expected the DELETE to reach the target after both connections were replaced")

	if n := obs.EventCount("ReBaselineTriggered"); n != 1 {
		t.Errorf("expected exactly 1 re-baseline, got %d", n)
	}
}

// TestPGSource_EphemeralReconnect_givesUpOnAPermanentStreamError drops the
// publication, so every replacement slot streams for a moment and then fails
// for the same reason. A new slot cannot fix that, and the source must stop
// after MaxAttempts slot losses in a row instead of re-baselining forever.
func TestPGSource_EphemeralReconnect_givesUpOnAPermanentStreamError(t *testing.T) {
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 2)

	ctx := context.Background()
	target := memory.NewIndexedTarget(memory.LookupFields("name"))
	obs := &testutil.TestObserver{}

	const maxAttempts = 3
	pubName := "laredo_ephemeral_permanent_pub"
	src := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotModeOpt(pg.SlotEphemeral),
		pg.SlotName("laredo_ephemeral_permanent_slot"),
		pg.Publication(pg.PublicationConfig{Name: pubName, Create: true}),
		pg.Reconnect(pg.ReconnectConfig{MaxAttempts: maxAttempts, InitialBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}),
	)
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
		laredo.WithObserver(obs),
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

	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "DROP PUBLICATION "+pubName); err != nil {
		t.Fatalf("drop publication: %v", err)
	}

	// Keep changes flowing so every stream reaches a change it cannot decode.
	wctx, stopWrites := context.WithCancel(ctx)
	writesDone := make(chan struct{})
	go func() {
		defer close(writesDone)
		w, err := pgx.Connect(wctx, pgc.ConnStr)
		if err != nil {
			return
		}
		defer func() { _ = w.Close(context.Background()) }()
		for n := 0; wctx.Err() == nil; n++ {
			_, _ = w.Exec(wctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", fmt.Sprintf("w-%d", n), "w@example.com")
			time.Sleep(20 * time.Millisecond)
		}
	}()
	defer func() { stopWrites(); <-writesDone }()

	testutil.AssertEventually(t, 60*time.Second, func() bool {
		for _, e := range obs.EventsByType("SourceDisconnected") {
			if r, _ := e.Data["reason"].(string); strings.Contains(r, "baseline failed") && strings.Contains(r, "times in a row") {
				return true
			}
		}
		return false
	}, "expected the source to give up after repeated slot losses")

	// It stays given up: no further re-baselines.
	n := obs.EventCount("ReBaselineTriggered")
	time.Sleep(2 * time.Second)
	if after := obs.EventCount("ReBaselineTriggered"); after != n {
		t.Errorf("re-baselines kept coming after the source gave up: %d then %d", n, after)
	}
	if n != maxAttempts+1 {
		t.Errorf("expected %d re-baselines (the last one refused), got %d", maxAttempts+1, n)
	}
}
