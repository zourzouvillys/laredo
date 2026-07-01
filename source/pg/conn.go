package pg

import (
	"context"
	"fmt"
	"strings"

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

	if cm.cfg.beforeConnect != nil {
		// pgx.ConnConfig embeds pgconn.Config by value; take its address so
		// hook mutations are visible to the subsequent ConnectConfig call.
		if err := cm.cfg.beforeConnect(ctx, &connCfg.Config); err != nil {
			return fmt.Errorf("before connect hook: %w", err)
		}
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

// baseline performs a consistent-point-in-time snapshot of the given tables
// using a REPEATABLE READ transaction, then SELECTs all rows from each table
// and delivers them via the rowCallback.
//
// If snapshotName is non-empty, it imports that exported snapshot (SET
// TRANSACTION SNAPSHOT) so the copy reads at exactly the replication slot's
// consistent point; the returned LSN is unused in that case (the caller streams
// from the slot's consistent point instead). If snapshotName is empty, it
// captures the current WAL position as the point-in-time and returns it.
func (cm *connManager) baseline(ctx context.Context, tables []laredo.TableIdentifier, snapshotName string, rowCallback func(laredo.TableIdentifier, laredo.Row)) (LSN, error) {
	tx, err := cm.queryConn.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback on error path

	// SET TRANSACTION SNAPSHOT must run before any query in the transaction.
	if snapshotName != "" {
		if _, err := tx.Exec(ctx, "SET TRANSACTION SNAPSHOT "+pgQuoteLiteral(snapshotName)); err != nil {
			return 0, fmt.Errorf("import exported snapshot %q: %w", snapshotName, err)
		}
	}

	// Without an exported snapshot to anchor to, capture the current WAL
	// position as this copy's point-in-time (the stream-start LSN).
	var lsn LSN
	if snapshotName == "" {
		var lsnStr string
		if err := tx.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&lsnStr); err != nil {
			return 0, fmt.Errorf("get current LSN: %w", err)
		}
		if lsn, err = ParseLSN(lsnStr); err != nil {
			return 0, fmt.Errorf("parse LSN %q: %w", lsnStr, err)
		}
	}

	// Read all rows from each table.
	for _, table := range tables {
		if err := cm.baselineTable(ctx, tx, table, rowCallback); err != nil {
			return 0, fmt.Errorf("baseline %s: %w", table, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return lsn, nil
}

// baselineTable reads all rows from a single table and delivers them via callback.
func (cm *connManager) baselineTable(ctx context.Context, tx pgx.Tx, table laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) error {
	query := fmt.Sprintf("SELECT * FROM %s.%s", pgQuoteIdent(table.Schema), pgQuoteIdent(table.Table))

	rows, err := tx.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	fieldDescs := rows.FieldDescriptions()
	colNames := make([]string, len(fieldDescs))
	for i, fd := range fieldDescs {
		colNames[i] = fd.Name
	}

	for rows.Next() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		values, err := rows.Values()
		if err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		row := make(laredo.Row, len(values))
		for i, v := range values {
			if i < len(colNames) {
				row[colNames[i]] = v
			}
		}

		rowCallback(table, row)
	}

	return rows.Err()
}

// pgQuoteIdent quotes a PostgreSQL identifier to prevent SQL injection.
func pgQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// pgQuoteLiteral quotes a PostgreSQL string literal to prevent SQL injection.
// Used for values that cannot be passed as bind parameters, such as the
// argument to SET TRANSACTION SNAPSHOT.
func pgQuoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
