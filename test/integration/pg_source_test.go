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

func TestPGSource_Init_SchemaDiscovery(t *testing.T) {
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)

	src := pg.New(pg.Connection(pgc.ConnStr))
	table := laredo.Table("public", "test_users")

	schemas, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{table},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	cols, ok := schemas[table]
	if !ok {
		t.Fatal("expected schema for public.test_users")
	}

	if len(cols) != 4 {
		t.Fatalf("expected 4 columns (id, name, email, created_at), got %d", len(cols))
	}

	// Verify column details.
	colMap := make(map[string]laredo.ColumnDefinition, len(cols))
	for _, c := range cols {
		colMap[c.Name] = c
	}

	idCol := colMap["id"]
	if !idCol.PrimaryKey {
		t.Error("expected id to be primary key")
	}
	if idCol.Nullable {
		t.Error("expected id to be non-nullable")
	}

	nameCol := colMap["name"]
	if nameCol.PrimaryKey {
		t.Error("expected name to not be primary key")
	}
	if nameCol.Nullable {
		t.Error("expected name to be non-nullable (NOT NULL)")
	}

	emailCol := colMap["email"]
	if !emailCol.Nullable {
		t.Error("expected email to be nullable")
	}

	if err := src.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPGSource_Baseline(t *testing.T) {
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 5)

	src := pg.New(pg.Connection(pgc.ConnStr))
	table := laredo.Table("public", "test_users")

	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{table},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Run baseline.
	var rows []laredo.Row
	pos, err := src.Baseline(context.Background(), []laredo.TableIdentifier{table}, func(tbl laredo.TableIdentifier, row laredo.Row) {
		if tbl != table {
			t.Errorf("unexpected table: %v", tbl)
		}
		rows = append(rows, row)
	})
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}

	// Should have received 5 rows.
	if len(rows) != 5 {
		t.Fatalf("expected 5 baseline rows, got %d", len(rows))
	}

	// Position should be a valid LSN.
	if pos == nil {
		t.Fatal("expected non-nil position")
	}
	lsnStr := src.PositionToString(pos)
	if lsnStr == "" {
		t.Error("expected non-empty LSN string")
	}
	t.Logf("baseline LSN: %s", lsnStr)

	// Verify row data.
	for i, row := range rows {
		name, ok := row["name"]
		if !ok {
			t.Errorf("row %d: missing 'name' field", i)
			continue
		}
		t.Logf("row %d: name=%v, email=%v", i, name, row["email"])
	}

	if err := src.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPGSource_BaselineEmpty(t *testing.T) {
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t) // Empty table, no rows inserted.

	src := pg.New(pg.Connection(pgc.ConnStr))
	table := laredo.Table("public", "test_users")

	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{table},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var rowCount int
	pos, err := src.Baseline(context.Background(), []laredo.TableIdentifier{table}, func(_ laredo.TableIdentifier, _ laredo.Row) {
		rowCount++
	})
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}

	if rowCount != 0 {
		t.Errorf("expected 0 rows from empty table, got %d", rowCount)
	}
	if pos == nil {
		t.Fatal("expected non-nil position even for empty baseline")
	}

	if err := src.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPGSource_EngineIntegration(t *testing.T) {
	// Full integration: PG source → engine → indexed memory target.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 3)

	target := memory.NewIndexedTarget(memory.LookupFields("name"))
	obs := &testutil.TestObserver{}

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", pg.New(pg.Connection(pgc.ConnStr))),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for baseline to complete and pipeline to be ready.
	if !eng.AwaitReady(30 * time.Second) {
		t.Fatal("engine did not become ready within 30s")
	}

	// Verify data arrived at the target.
	if target.Count() != 3 {
		t.Fatalf("expected 3 rows in target, got %d", target.Count())
	}

	// Verify we can look up by name.
	row, ok := target.Lookup("user-1")
	if !ok {
		t.Fatal("expected to find user-1")
	}
	if row["email"] != "user1@example.com" {
		t.Errorf("expected email=user1@example.com, got %v", row["email"])
	}

	// Verify observer events.
	if obs.EventCount("BaselineCompleted") != 1 {
		t.Errorf("expected 1 BaselineCompleted, got %d", obs.EventCount("BaselineCompleted"))
	}
	if obs.EventCount("SourceConnected") != 1 {
		t.Errorf("expected 1 SourceConnected, got %d", obs.EventCount("SourceConnected"))
	}

	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestPGSource_Streaming(t *testing.T) {
	// Full end-to-end: baseline + streaming changes via logical replication.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 2) // 2 baseline rows

	target := memory.NewIndexedTarget(memory.LookupFields("name"))
	obs := &testutil.TestObserver{}

	// Use a named slot with a publication.
	src := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotName("laredo_test_slot"),
		pg.Publication(pg.PublicationConfig{Name: "laredo_test_pub", Create: true}),
	)

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
		laredo.WithObserver(obs),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for baseline to complete.
	if !eng.AwaitReady(30 * time.Second) {
		t.Fatal("engine did not become ready")
	}

	if target.Count() != 2 {
		t.Fatalf("expected 2 baseline rows, got %d", target.Count())
	}
	t.Logf("baseline complete: %d rows", target.Count())

	// Now insert a new row via a separate connection (simulating app writes).
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect for writes: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "streamed-user", "streamed@example.com")
	if err != nil {
		t.Fatalf("insert streamed row: %v", err)
	}
	t.Log("inserted streamed row")

	// Wait for the streaming change to arrive at the target.
	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows after streaming insert")

	// Verify the streamed row.
	row, ok := target.Lookup("streamed-user")
	if !ok {
		t.Fatal("expected to find streamed-user via lookup")
	}
	t.Logf("streamed row: %v", row)

	// Insert another row and verify.
	_, err = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "user-4", "user4@example.com")
	if err != nil {
		t.Fatalf("insert second streamed row: %v", err)
	}

	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return target.Count() == 4
	}, "expected 4 rows after second streaming insert")

	// Update a row.
	_, err = conn.Exec(ctx, "UPDATE test_users SET email = $1 WHERE name = $2", "updated@example.com", "user-1")
	if err != nil {
		t.Fatalf("update row: %v", err)
	}

	testutil.AssertEventually(t, 15*time.Second, func() bool {
		r, ok := target.Lookup("user-1")
		return ok && fmt.Sprintf("%v", r["email"]) == "updated@example.com"
	}, "expected user-1 email to be updated")

	// Delete a row.
	_, err = conn.Exec(ctx, "DELETE FROM test_users WHERE name = $1", "user-4")
	if err != nil {
		t.Fatalf("delete row: %v", err)
	}

	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return target.Count() == 3
	}, "expected 3 rows after delete")

	// Verify observer received streaming events.
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return obs.EventCount("ChangeApplied") >= 3
	}, "expected at least 3 ChangeApplied events (insert + update + delete)")

	t.Logf("streaming test complete: %d ChangeApplied events", obs.EventCount("ChangeApplied"))

	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestPGSource_StatefulMode_Resume(t *testing.T) {
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 2)

	ctx := context.Background()
	slotName := "laredo_stateful_test"
	pubName := "laredo_stateful_pub"

	// First run: baseline + stream.
	src1 := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotModeOpt(pg.SlotStateful),
		pg.SlotName(slotName),
		pg.Publication(pg.PublicationConfig{Name: pubName, Create: true}),
	)
	target1 := memory.NewIndexedTarget(memory.LookupFields("name"))
	obs1 := &testutil.TestObserver{}

	eng1, errs := laredo.NewEngine(
		laredo.WithSource("pg", src1),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target1),
		laredo.WithObserver(obs1),
	)
	if len(errs) > 0 {
		t.Fatalf("engine1 errors: %v", errs)
	}

	if err := eng1.Start(ctx); err != nil {
		t.Fatalf("start1: %v", err)
	}
	if !eng1.AwaitReady(30 * time.Second) {
		t.Fatal("engine1 did not become ready")
	}
	if target1.Count() != 2 {
		t.Fatalf("expected 2 baseline rows, got %d", target1.Count())
	}
	t.Logf("first run: %d rows", target1.Count())

	// Insert while running.
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	_, _ = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "streamed-1", "s@e.com")
	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return target1.Count() == 3
	}, "expected 3 rows")

	if err := eng1.Stop(ctx); err != nil {
		t.Fatalf("stop1: %v", err)
	}
	conn.Close(ctx)

	// Verify source reports resume support.
	src2 := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotModeOpt(pg.SlotStateful),
		pg.SlotName(slotName),
		pg.Publication(pg.PublicationConfig{Name: pubName, Create: true}),
	)
	if !src2.SupportsResume() {
		t.Fatal("expected SupportsResume=true")
	}

	// Second run with same slot — should find existing slot.
	target2 := memory.NewIndexedTarget(memory.LookupFields("name"))
	eng2, errs := laredo.NewEngine(
		laredo.WithSource("pg", src2),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target2),
	)
	if len(errs) > 0 {
		t.Fatalf("engine2 errors: %v", errs)
	}

	if err := eng2.Start(ctx); err != nil {
		t.Fatalf("start2: %v", err)
	}
	if !eng2.AwaitReady(30 * time.Second) {
		t.Fatal("engine2 did not become ready")
	}

	t.Logf("second run: %d rows, resume mode", target2.Count())

	if err := eng2.Stop(ctx); err != nil {
		t.Fatalf("stop2: %v", err)
	}
}
