//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

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
