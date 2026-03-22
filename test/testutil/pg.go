//go:build integration

package testutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// PGContainer holds a running PostgreSQL testcontainer and its connection string.
type PGContainer struct {
	Container *postgres.PostgresContainer
	ConnStr   string
}

// NewTestPostgres starts a PostgreSQL testcontainer with wal_level=logical
// enabled for logical replication support. The container is automatically
// terminated when the test finishes.
func NewTestPostgres(t *testing.T) *PGContainer {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("laredo_test"),
		postgres.WithUsername("laredo"),
		postgres.WithPassword("laredo"),
		postgres.BasicWaitStrategies(),
		// Pass -c flags to enable logical replication.
		// The postgres Docker entrypoint forwards extra args to the postgres process.
		postgres.WithInitScripts(), // no-op, ensures standard entrypoint
		testcontainers.CustomizeRequest(testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Cmd: []string{
					"-c", "wal_level=logical",
					"-c", "max_replication_slots=10",
					"-c", "max_wal_senders=10",
				},
			},
		}),
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

	// Set REPLICA IDENTITY FULL so DELETE/UPDATE include old values.
	_, err = conn.Exec(ctx, `ALTER TABLE test_users REPLICA IDENTITY FULL`)
	if err != nil {
		t.Fatalf("set replica identity: %v", err)
	}
}

// NewTestPostgresWithWALLimit starts a PostgreSQL testcontainer with a small
// max_slot_wal_keep_size for testing slot invalidation scenarios.
func NewTestPostgresWithWALLimit(t *testing.T, walKeepSize string) *PGContainer {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("laredo_test"),
		postgres.WithUsername("laredo"),
		postgres.WithPassword("laredo"),
		postgres.BasicWaitStrategies(),
		testcontainers.CustomizeRequest(testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Cmd: []string{
					"-c", "wal_level=logical",
					"-c", "max_replication_slots=10",
					"-c", "max_wal_senders=10",
					"-c", "max_slot_wal_keep_size=" + walKeepSize,
				},
			},
		}),
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
