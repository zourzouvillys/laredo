// Package fanout implements a SyncTarget that multiplexes one source to N gRPC clients
// via snapshot + journal + live stream replication.
package fanout

import (
	"context"

	"github.com/zourzouvillys/laredo"
)

// Target implements laredo.SyncTarget as a replication fan-out multiplexer.
type Target struct {
	// TODO: implement
}

// New creates a new replication fan-out target.
func New( /* opts */ ) *Target {
	return &Target{}
}

var _ laredo.SyncTarget = (*Target)(nil)

//nolint:revive // implements SyncTarget.
func (t *Target) OnInit(ctx context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnBaselineRow(ctx context.Context, table laredo.TableIdentifier, row laredo.Row) error {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnBaselineComplete(ctx context.Context, table laredo.TableIdentifier) error {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnInsert(ctx context.Context, table laredo.TableIdentifier, columns laredo.Row) error {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnUpdate(ctx context.Context, table laredo.TableIdentifier, columns laredo.Row, identity laredo.Row) error {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnDelete(ctx context.Context, table laredo.TableIdentifier, identity laredo.Row) error {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) OnTruncate(ctx context.Context, table laredo.TableIdentifier) error {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) IsDurable() bool { return true }

//nolint:revive // implements SyncTarget.
func (t *Target) OnSchemaChange(ctx context.Context, table laredo.TableIdentifier, oldColumns, newColumns []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	return laredo.SchemaChangeResponse{Action: laredo.SchemaContinue}
}

//nolint:revive // implements SyncTarget.
func (t *Target) ExportSnapshot(ctx context.Context) ([]laredo.SnapshotEntry, error) {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) RestoreSnapshot(ctx context.Context, metadata laredo.TableSnapshotInfo, entries []laredo.SnapshotEntry) error {
	panic("not implemented")
}

//nolint:revive // implements SyncTarget.
func (t *Target) SupportsConsistentSnapshot() bool { return false }

//nolint:revive // implements SyncTarget.
func (t *Target) OnClose(ctx context.Context, table laredo.TableIdentifier) error {
	return nil
}
