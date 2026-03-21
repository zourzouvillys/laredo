package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/zourzouvillys/laredo"
)

// connManager manages the two PostgreSQL connections needed by the source:
// a query connection for baseline SELECTs and schema discovery, and a
// replication connection for streaming WAL changes.
type connManager struct {
	cfg sourceConfig

	queryConn *pgx.Conn
}

// connect establishes the query connection. The replication connection is
// established lazily when streaming begins.
func (cm *connManager) connect(ctx context.Context) error {
	connCfg, err := pgx.ParseConfig(cm.cfg.connString)
	if err != nil {
		return fmt.Errorf("parse connection string: %w", err)
	}

	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	cm.queryConn = conn
	return nil
}

// close closes all open connections.
func (cm *connManager) close(ctx context.Context) error {
	if cm.queryConn != nil {
		return cm.queryConn.Close(ctx)
	}
	return nil
}

// discoverSchemas queries pg_catalog to discover column definitions for the
// given tables. Returns a map from table to column definitions.
func (cm *connManager) discoverSchemas(ctx context.Context, tables []laredo.TableIdentifier) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	schemas := make(map[laredo.TableIdentifier][]laredo.ColumnDefinition, len(tables))

	for _, table := range tables {
		cols, err := cm.discoverTableColumns(ctx, table)
		if err != nil {
			return nil, fmt.Errorf("discover schema for %s: %w", table, err)
		}
		schemas[table] = cols
	}

	return schemas, nil
}

// discoverTableColumns queries column information for a single table.
func (cm *connManager) discoverTableColumns(ctx context.Context, table laredo.TableIdentifier) ([]laredo.ColumnDefinition, error) {
	const query = `
		SELECT
			c.ordinal_position,
			c.column_name,
			c.data_type,
			c.udt_name,
			c.is_nullable,
			c.column_default,
			c.character_maximum_length,
			COALESCE(tc_pk.ordinal_position, 0) AS pk_ordinal
		FROM information_schema.columns c
		LEFT JOIN (
			SELECT ku.column_name, ku.ordinal_position
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage ku
				ON tc.constraint_name = ku.constraint_name
				AND tc.table_schema = ku.table_schema
			WHERE tc.table_schema = $1
				AND tc.table_name = $2
				AND tc.constraint_type = 'PRIMARY KEY'
		) tc_pk ON tc_pk.column_name = c.column_name
		WHERE c.table_schema = $1
			AND c.table_name = $2
		ORDER BY c.ordinal_position
	`

	rows, err := cm.queryConn.Query(ctx, query, table.Schema, table.Table)
	if err != nil {
		return nil, fmt.Errorf("query columns: %w", err)
	}
	defer rows.Close()

	var cols []laredo.ColumnDefinition
	for rows.Next() {
		var (
			ordinal   int
			name      string
			dataType  string
			udtName   string
			nullable  string
			dflt      *string
			maxLen    *int
			pkOrdinal int
		)

		if err := rows.Scan(&ordinal, &name, &dataType, &udtName, &nullable, &dflt, &maxLen, &pkOrdinal); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}

		col := laredo.ColumnDefinition{
			Name:              name,
			Type:              udtName,
			Nullable:          nullable == "YES",
			PrimaryKey:        pkOrdinal > 0,
			OrdinalPosition:   ordinal,
			PrimaryKeyOrdinal: pkOrdinal,
		}
		col.DefaultValue = dflt
		if maxLen != nil {
			col.MaxLength = *maxLen
		}
		cols = append(cols, col)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate columns: %w", err)
	}

	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s not found or has no columns", table)
	}

	return cols, nil
}
