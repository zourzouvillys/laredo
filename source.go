package laredo

import (
	"context"
	"time"
)

// SourceState represents the connection/streaming state of a source.
type SourceState int

// Source connection states.
const (
	SourceConnecting   SourceState = iota // Initial connection or reconnecting after failure.
	SourceConnected                       // Connected, not yet streaming.
	SourceStreaming                       // Actively streaming changes.
	SourceReconnecting                    // Lost connection, attempting to reconnect.
	SourcePaused                          // Paused by engine request.
	SourceError                           // Unrecoverable error.
	SourceClosed                          // Shut down.
)

// OrderingGuarantee describes the ordering semantics a source provides.
type OrderingGuarantee int

// Source ordering guarantees.
const (
	TotalOrder        OrderingGuarantee = iota // All changes in commit order (PostgreSQL).
	PerTableOrder                              // Ordered within a table, no cross-table guarantee.
	PerPartitionOrder                          // Ordered within a partition/shard (Kinesis, Kafka).
	BestEffort                                 // No strong ordering.
)

// LagInfo reports health and lag information for a source.
type LagInfo struct {
	LagBytes int64
	LagTime  *time.Duration
}

// ChangeHandler receives change events from a source during streaming.
type ChangeHandler interface {
	OnChange(event ChangeEvent) error
}

// ChangeHandlerFunc adapts a function to the ChangeHandler interface.
type ChangeHandlerFunc func(ChangeEvent) error

// OnChange implements ChangeHandler.
func (f ChangeHandlerFunc) OnChange(event ChangeEvent) error {
	return f(event)
}

// SourceConfig holds configuration for source initialization.
type SourceConfig struct {
	Tables []TableIdentifier
}

// SyncSource produces baseline snapshots and change streams from a data source.
type SyncSource interface {
	// Init establishes a connection and discovers table schemas.
	Init(ctx context.Context, config SourceConfig) (map[TableIdentifier][]ColumnDefinition, error)

	// ValidateTables checks that the configured tables exist and are accessible.
	ValidateTables(ctx context.Context, tables []TableIdentifier) []ValidationError

	// Baseline performs a consistent point-in-time snapshot of the given tables,
	// calling rowCallback for each row. Returns the position marking the end of the snapshot.
	Baseline(ctx context.Context, tables []TableIdentifier, rowCallback func(TableIdentifier, Row)) (Position, error)

	// Stream begins streaming changes from the given position. Blocking.
	Stream(ctx context.Context, from Position, handler ChangeHandler) error

	// Ack acknowledges that all changes up to this position have been durably processed.
	Ack(ctx context.Context, position Position) error

	// SupportsResume reports whether this source can resume from a previously ACKed position.
	SupportsResume() bool

	// LastAckedPosition returns the last ACKed position, or nil if none exists.
	LastAckedPosition(ctx context.Context) (Position, error)

	// ComparePositions compares two positions. Returns negative if a < b, zero if equal, positive if a > b.
	ComparePositions(a, b Position) int

	// PositionToString serializes a position for storage or display.
	PositionToString(p Position) string

	// PositionFromString deserializes a position from a string.
	PositionFromString(s string) (Position, error)

	// Pause pauses the change stream without disconnecting.
	Pause(ctx context.Context) error

	// Resume resumes after a pause.
	Resume(ctx context.Context) error

	// GetLag returns health and lag information.
	GetLag() LagInfo

	// OrderingGuarantee returns the ordering semantics this source provides.
	OrderingGuarantee() OrderingGuarantee

	// State returns the current connection/streaming state.
	State() SourceState

	// Close performs a clean shutdown.
	Close(ctx context.Context) error
}
