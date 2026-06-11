package laredo

import "context"

// SchemaChangeAction describes how a target responds to a schema change.
type SchemaChangeAction int

// Schema change response actions.
const (
	SchemaContinue   SchemaChangeAction = iota // Target adapted, keep streaming.
	SchemaReBaseline                           // Target needs full reload.
	SchemaError                                // Target can't handle this change.
)

// SchemaChangeResponse is a target's response to a schema change notification.
type SchemaChangeResponse struct {
	Action  SchemaChangeAction
	Message string
}

// SyncTarget consumes baseline rows and change events for a table.
type SyncTarget interface {
	// OnInit is called once before baseline rows are delivered with the full column schema.
	OnInit(ctx context.Context, table TableIdentifier, columns []ColumnDefinition) error

	// OnBaselineRow is called for each row during the baseline snapshot phase.
	OnBaselineRow(ctx context.Context, table TableIdentifier, row Row) error

	// OnBaselineComplete is called after all baseline rows have been delivered.
	OnBaselineComplete(ctx context.Context, table TableIdentifier) error

	// OnInsert is called when a new row is inserted from the change stream.
	OnInsert(ctx context.Context, table TableIdentifier, columns Row) error

	// OnUpdate is called when an existing row is updated from the change stream.
	OnUpdate(ctx context.Context, table TableIdentifier, columns Row, identity Row) error

	// OnDelete is called when a row is deleted from the change stream.
	OnDelete(ctx context.Context, table TableIdentifier, identity Row) error

	// OnTruncate is called when the table is truncated.
	OnTruncate(ctx context.Context, table TableIdentifier) error

	// IsDurable reports whether the last applied change has been durably persisted.
	IsDurable() bool

	// OnSchemaChange is called when the source detects a column schema change.
	OnSchemaChange(ctx context.Context, table TableIdentifier, oldColumns, newColumns []ColumnDefinition) SchemaChangeResponse

	// ExportSnapshot exports all current state as a sequence of entries.
	ExportSnapshot(ctx context.Context) ([]SnapshotEntry, error)

	// RestoreSnapshot restores state from a snapshot.
	RestoreSnapshot(ctx context.Context, metadata TableSnapshotInfo, entries []SnapshotEntry) error

	// SupportsConsistentSnapshot reports whether this target can export a consistent
	// snapshot without requiring the engine to pause the stream.
	SupportsConsistentSnapshot() bool

	// OnClose is called on shutdown.
	OnClose(ctx context.Context, table TableIdentifier) error
}

// PositionedTarget is an optional interface a SyncTarget may implement to learn
// the source position of the change about to be delivered. When a target
// implements it, the engine calls SetChangePosition immediately before each
// corresponding OnInsert/OnUpdate/OnDelete/OnTruncate call.
//
// The engine guarantees these calls are ordered and sequential within a single
// pipeline, so a target may stash the position and read it back in the following
// On* call. A target bound to more than one table (i.e. shared across pipelines)
// must not rely on the stash, as positions from concurrent pipelines would race.
type PositionedTarget interface {
	// SetChangePosition records the source position of the next change to be
	// delivered to this target.
	SetChangePosition(position Position)
}
