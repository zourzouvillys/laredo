// Package memory implements compiled and indexed in-memory SyncTargets.
package memory

import (
	"context"

	"github.com/zourzouvillys/laredo"
)

// IndexedTarget implements laredo.SyncTarget as a schema-agnostic in-memory
// table replica with configurable secondary indexes.
type IndexedTarget struct {
	// TODO: implement
}

// NewIndexedTarget creates a new indexed in-memory target.
func NewIndexedTarget( /* opts */ ) *IndexedTarget {
	return &IndexedTarget{}
}

var _ laredo.SyncTarget = (*IndexedTarget)(nil)

func (t *IndexedTarget) OnInit(ctx context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	panic("not implemented")
}

func (t *IndexedTarget) OnBaselineRow(ctx context.Context, table laredo.TableIdentifier, row laredo.Row) error {
	panic("not implemented")
}

func (t *IndexedTarget) OnBaselineComplete(ctx context.Context, table laredo.TableIdentifier) error {
	panic("not implemented")
}

func (t *IndexedTarget) OnInsert(ctx context.Context, table laredo.TableIdentifier, columns laredo.Row) error {
	panic("not implemented")
}

func (t *IndexedTarget) OnUpdate(ctx context.Context, table laredo.TableIdentifier, columns laredo.Row, identity laredo.Row) error {
	panic("not implemented")
}

func (t *IndexedTarget) OnDelete(ctx context.Context, table laredo.TableIdentifier, identity laredo.Row) error {
	panic("not implemented")
}

func (t *IndexedTarget) OnTruncate(ctx context.Context, table laredo.TableIdentifier) error {
	panic("not implemented")
}

func (t *IndexedTarget) IsDurable() bool { return true }

func (t *IndexedTarget) OnSchemaChange(ctx context.Context, table laredo.TableIdentifier, oldColumns, newColumns []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	return laredo.SchemaChangeResponse{Action: laredo.SchemaReBaseline}
}

func (t *IndexedTarget) ExportSnapshot(ctx context.Context) ([]laredo.SnapshotEntry, error) {
	panic("not implemented")
}

func (t *IndexedTarget) RestoreSnapshot(ctx context.Context, metadata laredo.TableSnapshotInfo, entries []laredo.SnapshotEntry) error {
	panic("not implemented")
}

func (t *IndexedTarget) SupportsConsistentSnapshot() bool { return false }

func (t *IndexedTarget) OnClose(ctx context.Context, table laredo.TableIdentifier) error {
	return nil
}

// CompiledTarget implements laredo.SyncTarget by deserializing rows into
// strongly-typed domain objects via a pluggable compiler function.
type CompiledTarget struct {
	// TODO: implement
}

// NewCompiledTarget creates a new compiled in-memory target.
func NewCompiledTarget( /* opts */ ) *CompiledTarget {
	return &CompiledTarget{}
}

var _ laredo.SyncTarget = (*CompiledTarget)(nil)

func (t *CompiledTarget) OnInit(ctx context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	panic("not implemented")
}

func (t *CompiledTarget) OnBaselineRow(ctx context.Context, table laredo.TableIdentifier, row laredo.Row) error {
	panic("not implemented")
}

func (t *CompiledTarget) OnBaselineComplete(ctx context.Context, table laredo.TableIdentifier) error {
	panic("not implemented")
}

func (t *CompiledTarget) OnInsert(ctx context.Context, table laredo.TableIdentifier, columns laredo.Row) error {
	panic("not implemented")
}

func (t *CompiledTarget) OnUpdate(ctx context.Context, table laredo.TableIdentifier, columns laredo.Row, identity laredo.Row) error {
	panic("not implemented")
}

func (t *CompiledTarget) OnDelete(ctx context.Context, table laredo.TableIdentifier, identity laredo.Row) error {
	panic("not implemented")
}

func (t *CompiledTarget) OnTruncate(ctx context.Context, table laredo.TableIdentifier) error {
	panic("not implemented")
}

func (t *CompiledTarget) IsDurable() bool { return true }

func (t *CompiledTarget) OnSchemaChange(ctx context.Context, table laredo.TableIdentifier, oldColumns, newColumns []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	return laredo.SchemaChangeResponse{Action: laredo.SchemaReBaseline}
}

func (t *CompiledTarget) ExportSnapshot(ctx context.Context) ([]laredo.SnapshotEntry, error) {
	panic("not implemented")
}

func (t *CompiledTarget) RestoreSnapshot(ctx context.Context, metadata laredo.TableSnapshotInfo, entries []laredo.SnapshotEntry) error {
	panic("not implemented")
}

func (t *CompiledTarget) SupportsConsistentSnapshot() bool { return false }

func (t *CompiledTarget) OnClose(ctx context.Context, table laredo.TableIdentifier) error {
	return nil
}
