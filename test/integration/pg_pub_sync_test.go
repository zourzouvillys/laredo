//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestPGSource_PublicationSync(t *testing.T) {
	// Tests that the publication is synced on startup: tables are added/removed
	// to match the configured set without dropping the publication.
	pgc := testutil.NewTestPostgres(t)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Create two tables.
	_, err = conn.Exec(ctx, `
		CREATE TABLE test_users (
			id SERIAL PRIMARY KEY, name TEXT NOT NULL, email TEXT
		);
		ALTER TABLE test_users REPLICA IDENTITY FULL;

		CREATE TABLE test_orders (
			id SERIAL PRIMARY KEY, user_id INT NOT NULL, total NUMERIC
		);
		ALTER TABLE test_orders REPLICA IDENTITY FULL;
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}

	_, _ = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ('alice', 'a@b.com')")
	_, _ = conn.Exec(ctx, "INSERT INTO test_orders (user_id, total) VALUES (1, 99.99)")

	pubName := "sync_test_pub"
	slotName := "sync_test_slot"

	// Run 1: start with only test_users in the publication.
	src1 := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotName(slotName),
		pg.Publication(pg.PublicationConfig{Name: pubName, Create: true}),
	)
	target1 := memory.NewIndexedTarget()

	eng1, errs := laredo.NewEngine(
		laredo.WithSource("pg", src1),
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
	if target1.Count() != 1 {
		t.Fatalf("expected 1 user row, got %d", target1.Count())
	}
	if err := eng1.Stop(ctx); err != nil {
		t.Fatalf("stop1: %v", err)
	}
	t.Log("run 1: publication has test_users only")

	// Verify publication has exactly test_users.
	pubTables := getPublicationTables(t, conn, pubName)
	if !pubTables["public.test_users"] {
		t.Error("expected test_users in publication")
	}
	if pubTables["public.test_orders"] {
		t.Error("test_orders should not be in publication yet")
	}

	// Run 2: add test_orders, keep test_users. Publication should be synced.
	src2 := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotName(slotName),
		pg.Publication(pg.PublicationConfig{Name: pubName, Create: true}),
	)
	targetUsers := memory.NewIndexedTarget()
	targetOrders := memory.NewIndexedTarget()

	eng2, errs := laredo.NewEngine(
		laredo.WithSource("pg", src2),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), targetUsers),
		laredo.WithPipeline("pg", laredo.Table("public", "test_orders"), targetOrders),
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

	// Both tables should have data.
	if targetUsers.Count() != 1 {
		t.Errorf("expected 1 user, got %d", targetUsers.Count())
	}
	if targetOrders.Count() != 1 {
		t.Errorf("expected 1 order, got %d", targetOrders.Count())
	}

	// Publication should now have both tables.
	pubTables = getPublicationTables(t, conn, pubName)
	if !pubTables["public.test_users"] {
		t.Error("test_users should still be in publication")
	}
	if !pubTables["public.test_orders"] {
		t.Error("test_orders should have been added to publication")
	}

	if err := eng2.Stop(ctx); err != nil {
		t.Fatalf("stop2: %v", err)
	}
	t.Log("run 2: publication synced to test_users + test_orders")

	// Run 3: remove test_orders, keep only test_users.
	src3 := pg.New(
		pg.Connection(pgc.ConnStr),
		pg.SlotName(slotName),
		pg.Publication(pg.PublicationConfig{Name: pubName, Create: true}),
	)
	target3 := memory.NewIndexedTarget()

	eng3, errs := laredo.NewEngine(
		laredo.WithSource("pg", src3),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target3),
	)
	if len(errs) > 0 {
		t.Fatalf("engine3 errors: %v", errs)
	}

	if err := eng3.Start(ctx); err != nil {
		t.Fatalf("start3: %v", err)
	}
	if !eng3.AwaitReady(30 * time.Second) {
		t.Fatal("engine3 not ready")
	}
	if err := eng3.Stop(ctx); err != nil {
		t.Fatalf("stop3: %v", err)
	}

	// Publication should have only test_users now.
	pubTables = getPublicationTables(t, conn, pubName)
	if !pubTables["public.test_users"] {
		t.Error("test_users should still be in publication")
	}
	if pubTables["public.test_orders"] {
		t.Error("test_orders should have been removed from publication")
	}

	t.Log("run 3: publication synced back to test_users only")
}

func getPublicationTables(t *testing.T, conn *pgx.Conn, pubName string) map[string]bool {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		"SELECT schemaname, tablename FROM pg_publication_tables WHERE pubname = $1", pubName)
	if err != nil {
		t.Fatalf("query publication tables: %v", err)
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var schema, table string
		if err := rows.Scan(&schema, &table); err != nil {
			t.Fatalf("scan: %v", err)
		}
		result[schema+"."+table] = true
	}
	return result
}
