package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/zourzouvillys/laredo"
)

// replicationManager handles the logical replication protocol: slot creation,
// streaming pgoutput messages, and sending ACKs via StandbyStatusUpdate.
type replicationManager struct {
	cfg  sourceConfig
	conn *pgconn.PgConn

	// relation cache — pgoutput sends RELATION messages before data.
	relations map[uint32]*pglogrepl.RelationMessage
}

// connect establishes the replication connection.
func (rm *replicationManager) connect(ctx context.Context) error {
	connCfg, err := pgconn.ParseConfig(rm.cfg.connString)
	if err != nil {
		return fmt.Errorf("parse replication config: %w", err)
	}
	connCfg.RuntimeParams["replication"] = "database"

	if rm.cfg.beforeConnect != nil {
		if err := rm.cfg.beforeConnect(ctx, connCfg); err != nil {
			return fmt.Errorf("before connect hook: %w", err)
		}
	}

	conn, err := pgconn.ConnectConfig(ctx, connCfg)
	if err != nil {
		return fmt.Errorf("replication connect: %w", err)
	}

	rm.conn = conn
	rm.relations = make(map[uint32]*pglogrepl.RelationMessage)
	return nil
}

// close closes the replication connection.
func (rm *replicationManager) close(ctx context.Context) error {
	if rm.conn != nil {
		return rm.conn.Close(ctx)
	}
	return nil
}

// createSlot creates a logical replication slot using the pgoutput plugin.
// For ephemeral mode, the slot is temporary.
func (rm *replicationManager) createSlot(ctx context.Context, slotName string, temporary bool) (LSN, error) {
	result, err := pglogrepl.CreateReplicationSlot(ctx, rm.conn, slotName, "pgoutput",
		pglogrepl.CreateReplicationSlotOptions{
			Temporary: temporary,
		})
	if err != nil {
		return 0, fmt.Errorf("create slot %q: %w", slotName, err)
	}

	lsn, err := pglogrepl.ParseLSN(result.ConsistentPoint)
	if err != nil {
		return 0, fmt.Errorf("parse slot LSN: %w", err)
	}

	return LSN(lsn), nil
}

// dropSlot drops a replication slot.
func (rm *replicationManager) dropSlot(ctx context.Context, slotName string) error {
	return pglogrepl.DropReplicationSlot(ctx, rm.conn, slotName, pglogrepl.DropReplicationSlotOptions{})
}

// startStreaming begins logical replication from the given LSN.
// It decodes pgoutput messages and delivers them as ChangeEvents via the handler.
// Blocks until the context is cancelled or an error occurs.
func (rm *replicationManager) startStreaming(ctx context.Context, slotName string, pubName string, startLSN LSN, tables []laredo.TableIdentifier, handler laredo.ChangeHandler) error {
	err := pglogrepl.StartReplication(ctx, rm.conn, slotName, pglogrepl.LSN(startLSN),
		pglogrepl.StartReplicationOptions{
			PluginArgs: []string{
				"proto_version '1'",
				fmt.Sprintf("publication_names '%s'", pubName),
			},
		})
	if err != nil {
		return fmt.Errorf("start replication: %w", err)
	}

	// Build table lookup for filtering.
	tableSet := make(map[string]bool, len(tables))
	for _, t := range tables {
		tableSet[t.Schema+"."+t.Table] = true
	}

	var currentLSN LSN
	standbyDeadline := time.Now().Add(10 * time.Second)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Send periodic standby status to keep the connection alive.
		if time.Now().After(standbyDeadline) {
			if err := rm.sendStandbyStatus(ctx, currentLSN); err != nil {
				return fmt.Errorf("standby status: %w", err)
			}
			standbyDeadline = time.Now().Add(10 * time.Second)
		}

		recvCtx, cancel := context.WithDeadline(ctx, standbyDeadline)
		rawMsg, err := rm.conn.ReceiveMessage(recvCtx)
		cancel()

		if err != nil {
			if pgconn.Timeout(err) {
				continue // Timeout — send standby and try again.
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("receive message: %w", err)
		}

		switch msg := rawMsg.(type) {
		case *pgproto3.CopyData:
			switch msg.Data[0] {
			case pglogrepl.XLogDataByteID:
				xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
				if err != nil {
					return fmt.Errorf("parse xlog data: %w", err)
				}
				currentLSN = LSN(xld.WALStart + pglogrepl.LSN(len(xld.WALData)))

				if err := rm.handleWALData(ctx, xld.WALData, tableSet, handler); err != nil {
					return fmt.Errorf("handle WAL data: %w", err)
				}

			case pglogrepl.PrimaryKeepaliveMessageByteID:
				pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
				if err != nil {
					return fmt.Errorf("parse keepalive: %w", err)
				}
				if pkm.ReplyRequested {
					if err := rm.sendStandbyStatus(ctx, currentLSN); err != nil {
						return fmt.Errorf("standby status reply: %w", err)
					}
					standbyDeadline = time.Now().Add(10 * time.Second)
				}
			}

		case *pgproto3.ErrorResponse:
			return fmt.Errorf("replication error: %s: %s", msg.Code, msg.Message)
		}
	}
}

// sendStandbyStatus sends a StandbyStatusUpdate (ACK) for the given LSN.
func (rm *replicationManager) sendStandbyStatus(ctx context.Context, lsn LSN) error {
	return pglogrepl.SendStandbyStatusUpdate(ctx, rm.conn, pglogrepl.StandbyStatusUpdate{
		WALWritePosition: pglogrepl.LSN(lsn),
		WALFlushPosition: pglogrepl.LSN(lsn),
		WALApplyPosition: pglogrepl.LSN(lsn),
	})
}

// handleWALData decodes a single pgoutput message and dispatches change events.
func (rm *replicationManager) handleWALData(ctx context.Context, data []byte, tableSet map[string]bool, handler laredo.ChangeHandler) error {
	logicalMsg, err := pglogrepl.Parse(data)
	if err != nil {
		return fmt.Errorf("parse logical message: %w", err)
	}

	switch msg := logicalMsg.(type) {
	case *pglogrepl.RelationMessage:
		rm.relations[msg.RelationID] = msg

	case *pglogrepl.InsertMessage:
		rel, ok := rm.relations[msg.RelationID]
		if !ok {
			return nil // Unknown relation — skip.
		}
		tableName := rel.Namespace + "." + rel.RelationName
		if !tableSet[tableName] {
			return nil
		}
		row := tupleToRow(rel, msg.Tuple)
		return handler.OnChange(laredo.ChangeEvent{
			Table:     laredo.Table(rel.Namespace, rel.RelationName),
			Action:    laredo.ActionInsert,
			NewValues: row,
			Timestamp: time.Now(),
		})

	case *pglogrepl.UpdateMessage:
		rel, ok := rm.relations[msg.RelationID]
		if !ok {
			return nil
		}
		tableName := rel.Namespace + "." + rel.RelationName
		if !tableSet[tableName] {
			return nil
		}
		newRow := tupleToRow(rel, msg.NewTuple)
		var oldRow laredo.Row
		if msg.OldTuple != nil {
			oldRow = tupleToRow(rel, msg.OldTuple)
		}
		return handler.OnChange(laredo.ChangeEvent{
			Table:     laredo.Table(rel.Namespace, rel.RelationName),
			Action:    laredo.ActionUpdate,
			NewValues: newRow,
			OldValues: oldRow,
			Timestamp: time.Now(),
		})

	case *pglogrepl.DeleteMessage:
		rel, ok := rm.relations[msg.RelationID]
		if !ok {
			return nil
		}
		tableName := rel.Namespace + "." + rel.RelationName
		if !tableSet[tableName] {
			return nil
		}
		var oldRow laredo.Row
		if msg.OldTuple != nil {
			oldRow = tupleToRow(rel, msg.OldTuple)
		}
		return handler.OnChange(laredo.ChangeEvent{
			Table:     laredo.Table(rel.Namespace, rel.RelationName),
			Action:    laredo.ActionDelete,
			OldValues: oldRow,
			Timestamp: time.Now(),
		})

	case *pglogrepl.TruncateMessage:
		for _, relID := range msg.RelationIDs {
			rel, ok := rm.relations[relID]
			if !ok {
				continue
			}
			tableName := rel.Namespace + "." + rel.RelationName
			if !tableSet[tableName] {
				continue
			}
			if err := handler.OnChange(laredo.ChangeEvent{
				Table:     laredo.Table(rel.Namespace, rel.RelationName),
				Action:    laredo.ActionTruncate,
				Timestamp: time.Now(),
			}); err != nil {
				return err
			}
		}

	case *pglogrepl.BeginMessage, *pglogrepl.CommitMessage, *pglogrepl.TypeMessage, *pglogrepl.OriginMessage:
		// Transaction boundary messages — no action needed.
	}

	return nil
}

// tupleToRow converts a pgoutput tuple into a laredo.Row using the relation's
// column definitions.
func tupleToRow(rel *pglogrepl.RelationMessage, tuple *pglogrepl.TupleData) laredo.Row {
	if tuple == nil {
		return nil
	}
	row := make(laredo.Row, len(tuple.Columns))
	for i, col := range tuple.Columns {
		if i >= len(rel.Columns) {
			break
		}
		colName := rel.Columns[i].Name
		switch col.DataType {
		case 'n': // null
			row[colName] = nil
		case 't': // text
			row[colName] = string(col.Data)
		case 'u': // unchanged TOAST — skip
			continue
		}
	}
	return row
}
