package laredo

import (
	"context"
	"io"
	"time"
)

// SnapshotEntry is a single row in a snapshot.
type SnapshotEntry struct {
	Row Row
}

// SnapshotMetadata describes a complete snapshot.
type SnapshotMetadata struct {
	SnapshotID      string
	CreatedAt       time.Time
	SourcePositions map[string]Position // keyed by source ID
	Tables          []TableSnapshotInfo
	Format          string            // e.g. "jsonl"
	UserMeta        map[string]Value
}

// TableSnapshotInfo describes one table within a snapshot.
type TableSnapshotInfo struct {
	Table      TableIdentifier
	RowCount   int64
	Columns    []ColumnDefinition
	TargetType string
	Indexes    []IndexDefinition
}

// SnapshotDescriptor is a lightweight handle for a stored snapshot (metadata without row data).
type SnapshotDescriptor struct {
	SnapshotID string
	CreatedAt  time.Time
	Format     string
	Tables     []TableSnapshotInfo
	SizeBytes  int64
}

// SnapshotFilter constrains which snapshots to list.
type SnapshotFilter struct {
	Table *TableIdentifier
}

// SnapshotStore persists and retrieves snapshots.
type SnapshotStore interface {
	// Save writes a snapshot.
	Save(ctx context.Context, snapshotID string, metadata SnapshotMetadata, entries map[TableIdentifier][]SnapshotEntry) (SnapshotDescriptor, error)

	// Load reads a snapshot by ID.
	Load(ctx context.Context, snapshotID string) (SnapshotMetadata, map[TableIdentifier][]SnapshotEntry, error)

	// Describe returns snapshot metadata without loading row data.
	Describe(ctx context.Context, snapshotID string) (SnapshotDescriptor, error)

	// List returns available snapshots, optionally filtered.
	List(ctx context.Context, filter *SnapshotFilter) ([]SnapshotDescriptor, error)

	// Delete removes a snapshot.
	Delete(ctx context.Context, snapshotID string) error

	// Prune deletes all but the N most recent snapshots.
	Prune(ctx context.Context, keep int, table *TableIdentifier) error
}

// SnapshotSerializer handles serialization of snapshot data.
type SnapshotSerializer interface {
	// FormatID returns the format identifier (e.g. "jsonl").
	FormatID() string

	// Write serializes rows to the output stream.
	Write(info TableSnapshotInfo, rows []Row, w io.Writer) error

	// Read deserializes rows from the input stream.
	Read(r io.Reader) (TableSnapshotInfo, []Row, error)
}
