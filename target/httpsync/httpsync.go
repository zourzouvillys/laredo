// Package httpsync implements a SyncTarget that forwards changes as batched HTTP requests.
package httpsync

import (
	"context"

	"github.com/zourzouvillys/laredo"
)

// Target implements laredo.SyncTarget by forwarding changes via HTTP.
type Target struct {
	// TODO: implement
}

// New creates a new HTTP sync target.
func New( /* opts */ ) *Target {
	return &Target{}
}

var _ laredo.SyncTarget = (*Target)(nil)

func (t *Target) OnInit(ctx context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	panic("not implemented")
}

func (t *Target) OnBaselineRow(ctx context.Context, table laredo.TableIdentifier, row laredo.Row) error {
	panic("not implemented")
}

func (t *Target) OnBaselineComplete(ctx context.Context, table laredo.TableIdentifier) error {
	panic("not implemented")
}

func (t *Target) OnInsert(ctx context.Context, table laredo.TableIdentifier, columns laredo.Row) error {
	panic("not implemented")
}

func (t *Target) OnUpdate(ctx context.Context, table laredo.TableIdentifier, columns laredo.Row, identity laredo.Row) error {
	panic("not implemented")
}

func (t *Target) OnDelete(ctx context.Context, table laredo.TableIdentifier, identity laredo.Row) error {
	panic("not implemented")
}

func (t *Target) OnTruncate(ctx context.Context, table laredo.TableIdentifier) error {
	panic("not implemented")
}

func (t *Target) IsDurable() bool { return false }

func (t *Target) OnSchemaChange(ctx context.Context, table laredo.TableIdentifier, oldColumns, newColumns []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	return laredo.SchemaChangeResponse{Action: laredo.SchemaContinue}
}

func (t *Target) ExportSnapshot(ctx context.Context) ([]laredo.SnapshotEntry, error) {
	panic("not implemented")
}

func (t *Target) RestoreSnapshot(ctx context.Context, metadata laredo.TableSnapshotInfo, entries []laredo.SnapshotEntry) error {
	panic("not implemented")
}

func (t *Target) SupportsConsistentSnapshot() bool { return false }

func (t *Target) OnClose(ctx context.Context, table laredo.TableIdentifier) error {
	return nil
}
