package laredo

import (
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"time"
)

// ErrReBaselineRequired is returned by SyncSource.Stream to signal that the
// source's position is no longer valid and a full re-baseline is needed.
// The engine will re-run the baseline when it receives this error.
var ErrReBaselineRequired = errors.New("re-baseline required")

// TableIdentifier identifies a table within a data source.
type TableIdentifier struct {
	Schema string
	Table  string
}

// Table creates a TableIdentifier.
func Table(schema, table string) TableIdentifier {
	return TableIdentifier{Schema: schema, Table: table}
}

// String returns "schema.table".
func (t TableIdentifier) String() string {
	return t.Schema + "." + t.Table
}

// MarshalText implements encoding.TextMarshaler.
func (t TableIdentifier) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *TableIdentifier) UnmarshalText(text []byte) error {
	schema, table, ok := strings.Cut(string(text), ".")
	if !ok || schema == "" || table == "" {
		return fmt.Errorf("invalid table identifier %q: expected schema.table", string(text))
	}
	t.Schema = schema
	t.Table = table
	return nil
}

// ColumnDefinition describes a column in a table.
type ColumnDefinition struct {
	Name              string
	Type              string
	Nullable          bool
	PrimaryKey        bool
	OrdinalPosition   int     // 1-based column position; 0 = unset.
	PrimaryKeyOrdinal int     // 1-based position within composite PK; 0 = not a PK column.
	DefaultValue      *string // SQL default expression, nil if none.
	MaxLength         int     // Max length for variable-length types; 0 = unlimited.
	TypeOID           uint32  // Source-specific type OID (e.g., PostgreSQL OID); 0 = unset.
}

// Value represents an arbitrary column value.
type Value = any

// Row is a map of column names to values.
type Row map[string]Value

// GetString returns the string value for the given key, or empty string if not found or not a string.
func (r Row) GetString(key string) string {
	if v, ok := r[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Get returns the value for the given key and whether it was present.
func (r Row) Get(key string) (Value, bool) {
	v, ok := r[key]
	return v, ok
}

// Keys returns the column names in sorted order for deterministic iteration.
func (r Row) Keys() iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, k := range slices.Sorted(maps.Keys(r)) {
			if !yield(k) {
				return
			}
		}
	}
}

// Without returns a copy of the row without the specified keys.
func (r Row) Without(keys ...string) Row {
	drop := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		drop[k] = struct{}{}
	}
	out := make(Row, len(r))
	for k, v := range r {
		if _, ok := drop[k]; !ok {
			out[k] = v
		}
	}
	return out
}

// Position is an opaque source position token. Each source defines its own
// concrete representation (e.g., PostgreSQL LSN, Kinesis sequence number).
type Position = any

// ChangeAction describes the type of a row change.
type ChangeAction int

// Change action types.
const (
	ActionInsert   ChangeAction = iota // Row inserted.
	ActionUpdate                       // Row updated.
	ActionDelete                       // Row deleted.
	ActionTruncate                     // Table truncated.
)

// String returns the action name.
func (a ChangeAction) String() string {
	switch a {
	case ActionInsert:
		return "INSERT"
	case ActionUpdate:
		return "UPDATE"
	case ActionDelete:
		return "DELETE"
	case ActionTruncate:
		return "TRUNCATE"
	default:
		return "UNKNOWN"
	}
}

// ChangeEvent represents a single row change from a source.
type ChangeEvent struct {
	Table    TableIdentifier
	Action   ChangeAction
	Position Position

	// Timestamp is when the change was emitted by the source.
	Timestamp time.Time

	// CommitTimestamp is the commit time of the source transaction, if available.
	// For PostgreSQL, this is the WAL commit timestamp. Nil if the source does
	// not provide commit-level timestamps.
	CommitTimestamp *time.Time

	XID int64

	// NewValues contains column values after the change (insert, update).
	NewValues Row
	// OldValues contains column values before the change (update, delete).
	// May be nil if the source does not provide old values.
	OldValues Row
}

// IndexDefinition describes a secondary index on a target.
type IndexDefinition struct {
	Name   string
	Fields []string
	Unique bool
}
