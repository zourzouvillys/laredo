//go:build integration

package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PGContainer holds a running PostgreSQL testcontainer and its connection string.
type PGContainer struct {
	Container *postgres.PostgresContainer
	ConnStr   string
}

// NewTestPostgres starts a PostgreSQL testcontainer and returns the connection
// string. The container is automatically terminated when the test finishes.
func NewTestPostgres(t *testing.T) *PGContainer {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("laredo_test"),
		postgres.WithUsername("laredo"),
		postgres.WithPassword("laredo"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}

	t.Cleanup(func() {
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("terminate postgres: %v", err)
		}
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	return &PGContainer{
		Container: pgContainer,
		ConnStr:   connStr,
	}
}

// CreateTestTable creates a sample table in the PostgreSQL container for testing.
// Returns the table name as "public.test_users".
func (pg *PGContainer) CreateTestTable(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, pg.ConnStr)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS test_users (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	if err != nil {
		t.Fatalf("create test table: %v", err)
	}
}

// InsertTestRows inserts N sample rows into the test_users table.
func (pg *PGContainer) InsertTestRows(t *testing.T, count int) {
	t.Helper()
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, pg.ConnStr)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer conn.Close(ctx)

	for i := range count {
		_, err = conn.Exec(ctx,
			"INSERT INTO test_users (name, email) VALUES ($1, $2)",
			fmt.Sprintf("user-%d", i+1),
			fmt.Sprintf("user%d@example.com", i+1),
		)
		if err != nil {
			t.Fatalf("insert row %d: %v", i+1, err)
		}
	}
}
