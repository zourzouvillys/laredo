//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// TestPGSource_EngineRetry_databaseUnavailable models a database restart that
// outlasts the source's own reconnect budget: every connection the source holds
// is terminated and new connections are refused for a few seconds. The source
// gives up, and the engine must keep starting it again until the database is
// back, then stream live.
//
// A row is deleted while the database refuses the source. Once it recovers, the
// row must be gone: the re-baseline removes rows it no longer finds instead of
// only overwriting the ones it does.
func TestPGSource_EngineRetry_databaseUnavailable(t *testing.T) {
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 2)

	ctx := context.Background()
	// data writes to the test database and survives the outage (it is
	// excluded from the terminate below). admin runs from the postgres
	// database, because a session cannot refuse connections to its own.
	data, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer data.Close(ctx)
	var dbName string
	var dataPID int
	if err := data.QueryRow(ctx, "SELECT current_database(), pg_backend_pid()").Scan(&dbName, &dataPID); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	adminCfg, err := pgx.ParseConfig(pgc.ConnStr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	adminCfg.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := data.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "doomed", "d@x.com"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	target := memory.NewIndexedTarget(memory.LookupFields("name"))
	obs := &testutil.TestObserver{}
	src := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotModeOpt(pg.SlotEphemeral),
		pg.SlotName("laredo_engine_retry_slot"),
		pg.Publication(pg.PublicationConfig{Name: "laredo_engine_retry_pub", Create: true}),
		// One attempt: the source gives up at once, so recovery is the engine's.
		pg.Reconnect(pg.ReconnectConfig{MaxAttempts: 1, InitialBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}),
	)
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
		laredo.WithObserver(obs),
		laredo.WithSourceRetry(laredo.SourceRetry{InitialBackoff: 200 * time.Millisecond, MaxBackoff: time.Second}),
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
	if _, ok := target.Lookup("doomed"); !ok {
		t.Fatal("baseline missing the row")
	}

	// The restart: refuse new connections, terminate every existing one but
	// ours, and delete a row while the source cannot see it.
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{dbName}.Sanitize()+" WITH ALLOW_CONNECTIONS false"); err != nil {
		t.Fatalf("refuse connections: %v", err)
	}
	reallow := func() {
		_, _ = admin.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{dbName}.Sanitize()+" WITH ALLOW_CONNECTIONS true")
	}
	defer reallow()
	if _, err := admin.Exec(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid NOT IN (pg_backend_pid(), $2) AND (datname = $1 OR backend_type = 'walsender')", dbName, dataPID,
	); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if _, err := data.Exec(ctx, "DELETE FROM test_users WHERE name = $1", "doomed"); err != nil {
		t.Fatalf("delete during outage: %v", err)
	}

	// Several engine retries fail while the database refuses connections.
	testutil.AssertEventually(t, 30*time.Second, func() bool {
		n := 0
		for _, e := range obs.EventsByType("SourceDisconnected") {
			if r, _ := e.Data["reason"].(string); strings.Contains(r, "retrying in") {
				n++
			}
		}
		return n >= 3
	}, "expected the engine to keep retrying while the database was unavailable")
	if _, ok := target.Lookup("test_user_1"); !ok && target.Count() == 0 {
		t.Fatal("target emptied while the source was failing")
	}

	reallow()

	testutil.AssertEventually(t, 60*time.Second, func() bool {
		_, ok := target.Lookup("doomed")
		return !ok && eng.IsSourceReady("pg")
	}, "row deleted during the outage survived recovery")

	// Live, not merely re-baselined: an insert and then a delete both arrive.
	if _, err := data.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "after", "a@x.com"); err != nil {
		t.Fatalf("insert after recovery: %v", err)
	}
	testutil.AssertEventually(t, 30*time.Second, func() bool {
		_, ok := target.Lookup("after")
		return ok
	}, "insert after recovery did not arrive")
	if _, err := data.Exec(ctx, "DELETE FROM test_users WHERE name = $1", "after"); err != nil {
		t.Fatalf("delete after recovery: %v", err)
	}
	testutil.AssertEventually(t, 30*time.Second, func() bool {
		_, ok := target.Lookup("after")
		return !ok
	}, "delete after recovery did not arrive: not streaming")
}
