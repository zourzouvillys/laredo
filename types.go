package laredo

import "time"

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

// ColumnDefinition describes a column in a table.
type ColumnDefinition struct {
	Name       string
	Type       string
	Nullable   bool
	PrimaryKey bool
}

// Value represents an arbitrary column value.
type Value interface{}

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
type Position interface{}

// ChangeAction describes the type of a row change.
type ChangeAction int

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
	Table     TableIdentifier
	Action    ChangeAction
	Position  Position
	Timestamp time.Time
	XID       int64

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
