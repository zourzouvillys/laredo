// Package memory implements compiled and indexed in-memory SyncTargets.
package memory

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/zourzouvillys/laredo"
)

// IndexedTargetOption configures an IndexedTarget.
type IndexedTargetOption func(*IndexedTarget)

// LookupFields configures the primary lookup index (unique composite key).
// The lookup index provides fast access by the specified field values,
// separate from the primary key index.
func LookupFields(fields ...string) IndexedTargetOption {
	return func(t *IndexedTarget) {
		t.lookupFields = fields
	}
}

// AddIndex adds a secondary index to the target. Unique indexes map a
// composite key to a single row; non-unique indexes map to a slice of rows.
func AddIndex(def laredo.IndexDefinition) IndexedTargetOption {
	return func(t *IndexedTarget) {
		t.indexDefs = append(t.indexDefs, def)
	}
}

// secondaryIndex holds the runtime state for one secondary index.
type secondaryIndex struct {
	def     laredo.IndexDefinition
	unique  map[string]laredo.Row
	nonuniq map[string][]laredo.Row
}

// IndexedTarget implements laredo.SyncTarget as a schema-agnostic in-memory
// table replica with configurable secondary indexes.
//
// All write operations (OnBaselineRow, OnInsert, OnUpdate, OnDelete, OnTruncate)
// acquire the write lock. Read operations (Lookup, Get, All, Count) acquire the
// read lock. Listener callbacks run under the write lock and must not block.
type IndexedTarget struct {
	mu sync.RWMutex

	// Configuration (set before OnInit).
	lookupFields []string
	indexDefs    []laredo.IndexDefinition

	// Schema (set during OnInit).
	table   laredo.TableIdentifier
	columns []laredo.ColumnDefinition
	pkCols  []string // primary key column names in ordinal order

	// Primary store: composite PK string -> Row.
	store map[string]laredo.Row

	// Lookup index (unique, keyed by lookupFields).
	lookupIndex map[string]laredo.Row

	// Secondary indexes by name.
	indexes map[string]*secondaryIndex

	// Change listeners.
	listeners map[int]func(old, new laredo.Row)
	nextID    int
}

// NewIndexedTarget creates a new indexed in-memory target.
func NewIndexedTarget(opts ...IndexedTargetOption) *IndexedTarget {
	t := &IndexedTarget{
		listeners: make(map[int]func(old, new laredo.Row)),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

var _ laredo.SyncTarget = (*IndexedTarget)(nil)

// buildKey constructs a composite key by joining formatted values with \x00.
func buildKey(row laredo.Row, fields []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = fmt.Sprintf("%v", row[f])
	}
	return strings.Join(parts, "\x00")
}

// buildKeyFromValues constructs a composite key from raw values.
func buildKeyFromValues(values []laredo.Value) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprintf("%v", v)
	}
	return strings.Join(parts, "\x00")
}

// copyRow returns a shallow copy of a row.
func copyRow(r laredo.Row) laredo.Row {
	return maps.Clone(r)
}

// addToIndexes inserts a row into lookup and all secondary indexes.
func (t *IndexedTarget) addToIndexes(row laredo.Row) {
	if t.lookupIndex != nil {
		key := buildKey(row, t.lookupFields)
		t.lookupIndex[key] = row
	}
	for _, idx := range t.indexes {
		key := buildKey(row, idx.def.Fields)
		if idx.def.Unique {
			idx.unique[key] = row
		} else {
			idx.nonuniq[key] = append(idx.nonuniq[key], row)
		}
	}
}

// removeFromIndexes removes a row from lookup and all secondary indexes.
func (t *IndexedTarget) removeFromIndexes(row laredo.Row) {
	if t.lookupIndex != nil {
		key := buildKey(row, t.lookupFields)
		delete(t.lookupIndex, key)
	}
	for _, idx := range t.indexes {
		key := buildKey(row, idx.def.Fields)
		if idx.def.Unique {
			delete(idx.unique, key)
		} else {
			rows := idx.nonuniq[key]
			pkKey := buildKey(row, t.pkCols)
			rows = slices.DeleteFunc(rows, func(r laredo.Row) bool {
				return buildKey(r, t.pkCols) == pkKey
			})
			if len(rows) == 0 {
				delete(idx.nonuniq, key)
			} else {
				idx.nonuniq[key] = rows
			}
		}
	}
}

// notifyListeners calls all registered listeners with the old and new rows.
// Must be called under the write lock.
func (t *IndexedTarget) notifyListeners(old, new laredo.Row) {
	for _, fn := range t.listeners {
		fn(old, new)
	}
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnInit(_ context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.table = table
	t.columns = columns

	// Build column name set for validation.
	colSet := make(map[string]struct{}, len(columns))
	for _, c := range columns {
		colSet[c.Name] = struct{}{}
	}

	// Determine primary key columns, sorted by PrimaryKeyOrdinal.
	var pkCols []laredo.ColumnDefinition
	for _, c := range columns {
		if c.PrimaryKey {
			pkCols = append(pkCols, c)
		}
	}
	slices.SortFunc(pkCols, func(a, b laredo.ColumnDefinition) int {
		return a.PrimaryKeyOrdinal - b.PrimaryKeyOrdinal
	})
	t.pkCols = make([]string, len(pkCols))
	for i, c := range pkCols {
		t.pkCols[i] = c.Name
	}

	// Validate lookup fields.
	for _, f := range t.lookupFields {
		if _, ok := colSet[f]; !ok {
			return fmt.Errorf("lookup field %q not found in columns", f)
		}
	}

	// Validate secondary index fields.
	for _, def := range t.indexDefs {
		for _, f := range def.Fields {
			if _, ok := colSet[f]; !ok {
				return fmt.Errorf("index %q field %q not found in columns", def.Name, f)
			}
		}
	}

	// Allocate data structures.
	t.store = make(map[string]laredo.Row)

	if len(t.lookupFields) > 0 {
		t.lookupIndex = make(map[string]laredo.Row)
	}

	t.indexes = make(map[string]*secondaryIndex, len(t.indexDefs))
	for _, def := range t.indexDefs {
		idx := &secondaryIndex{def: def}
		if def.Unique {
			idx.unique = make(map[string]laredo.Row)
		} else {
			idx.nonuniq = make(map[string][]laredo.Row)
		}
		t.indexes[def.Name] = idx
	}

	return nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnBaselineRow(_ context.Context, _ laredo.TableIdentifier, row laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	pkKey := buildKey(row, t.pkCols)
	t.store[pkKey] = row
	t.addToIndexes(row)
	t.notifyListeners(nil, row)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnBaselineComplete(_ context.Context, _ laredo.TableIdentifier) error {
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnInsert(_ context.Context, _ laredo.TableIdentifier, columns laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	pkKey := buildKey(columns, t.pkCols)
	t.store[pkKey] = columns
	t.addToIndexes(columns)
	t.notifyListeners(nil, columns)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnUpdate(_ context.Context, _ laredo.TableIdentifier, columns laredo.Row, identity laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Find old row by identity (which contains at minimum the PK fields).
	oldPK := buildKey(identity, t.pkCols)
	oldRow, exists := t.store[oldPK]

	if exists {
		t.removeFromIndexes(oldRow)
		delete(t.store, oldPK)
	}

	// Store new row.
	newPK := buildKey(columns, t.pkCols)
	t.store[newPK] = columns
	t.addToIndexes(columns)
	t.notifyListeners(oldRow, columns)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnDelete(_ context.Context, _ laredo.TableIdentifier, identity laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	pkKey := buildKey(identity, t.pkCols)
	oldRow, exists := t.store[pkKey]
	if exists {
		t.removeFromIndexes(oldRow)
		delete(t.store, pkKey)
		t.notifyListeners(oldRow, nil)
	}
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnTruncate(_ context.Context, _ laredo.TableIdentifier) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	clear(t.store)
	if t.lookupIndex != nil {
		clear(t.lookupIndex)
	}
	for _, idx := range t.indexes {
		if idx.def.Unique {
			clear(idx.unique)
		} else {
			clear(idx.nonuniq)
		}
	}
	t.notifyListeners(nil, nil)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) IsDurable() bool { return true }

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnSchemaChange(_ context.Context, _ laredo.TableIdentifier, oldColumns, newColumns []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	// If all old columns are still present and there are new ones, we can continue.
	newSet := make(map[string]struct{}, len(newColumns))
	for _, c := range newColumns {
		newSet[c.Name] = struct{}{}
	}

	allOldPresent := true
	for _, c := range oldColumns {
		if _, ok := newSet[c.Name]; !ok {
			allOldPresent = false
			break
		}
	}

	if allOldPresent && len(newColumns) > len(oldColumns) {
		return laredo.SchemaChangeResponse{Action: laredo.SchemaContinue, Message: "new columns added"}
	}

	return laredo.SchemaChangeResponse{Action: laredo.SchemaReBaseline}
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) ExportSnapshot(_ context.Context) ([]laredo.SnapshotEntry, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	entries := make([]laredo.SnapshotEntry, 0, len(t.store))
	for _, row := range t.store {
		entries = append(entries, laredo.SnapshotEntry{Row: copyRow(row)})
	}
	return entries, nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) RestoreSnapshot(_ context.Context, _ laredo.TableSnapshotInfo, entries []laredo.SnapshotEntry) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Clear existing data.
	clear(t.store)
	if t.lookupIndex != nil {
		clear(t.lookupIndex)
	}
	for _, idx := range t.indexes {
		if idx.def.Unique {
			clear(idx.unique)
		} else {
			clear(idx.nonuniq)
		}
	}

	// Restore from entries.
	for _, entry := range entries {
		pkKey := buildKey(entry.Row, t.pkCols)
		t.store[pkKey] = entry.Row
		t.addToIndexes(entry.Row)
	}
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) SupportsConsistentSnapshot() bool { return true }

//nolint:revive // implements SyncTarget.
func (t *IndexedTarget) OnClose(_ context.Context, _ laredo.TableIdentifier) error {
	return nil
}

// Lookup returns the row matching the configured lookup fields, or false if not found.
// The returned row is a copy to prevent mutation.
func (t *IndexedTarget) Lookup(keyValues ...laredo.Value) (laredo.Row, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.lookupIndex == nil {
		return nil, false
	}

	key := buildKeyFromValues(keyValues)
	row, ok := t.lookupIndex[key]
	if !ok {
		return nil, false
	}
	return copyRow(row), true
}

// LookupAll returns all rows matching the given secondary index values.
// For a unique index, returns at most one row. For a non-unique index,
// returns all matching rows. The returned rows are copies to prevent mutation.
func (t *IndexedTarget) LookupAll(indexName string, keyValues ...laredo.Value) []laredo.Row {
	t.mu.RLock()
	defer t.mu.RUnlock()

	idx, ok := t.indexes[indexName]
	if !ok {
		return nil
	}

	key := buildKeyFromValues(keyValues)

	if idx.def.Unique {
		row, ok := idx.unique[key]
		if !ok {
			return nil
		}
		return []laredo.Row{copyRow(row)}
	}

	rows := idx.nonuniq[key]
	result := make([]laredo.Row, len(rows))
	for i, r := range rows {
		result[i] = copyRow(r)
	}
	return result
}

// Get returns the row with the given primary key values, or false if not found.
// The returned row is a copy to prevent mutation.
func (t *IndexedTarget) Get(pkValues ...laredo.Value) (laredo.Row, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	key := buildKeyFromValues(pkValues)
	row, ok := t.store[key]
	if !ok {
		return nil, false
	}
	return copyRow(row), true
}

// All returns an iterator over all rows in the store as (pk-key, row) pairs.
// The rows returned are copies to prevent mutation.
func (t *IndexedTarget) All() iter.Seq2[string, laredo.Row] {
	return func(yield func(string, laredo.Row) bool) {
		t.mu.RLock()
		defer t.mu.RUnlock()

		for k, v := range t.store {
			if !yield(k, copyRow(v)) {
				return
			}
		}
	}
}

// Count returns the number of rows in the store.
func (t *IndexedTarget) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.store)
}

// Listen registers a change listener that is called for every insert, update,
// delete, and truncate. For inserts, old is nil. For deletes, new is nil.
// For truncate, both are nil. The callback runs under the write lock and must
// not block. Returns an unsubscribe function.
func (t *IndexedTarget) Listen(fn func(old, new laredo.Row)) func() {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := t.nextID
	t.nextID++
	t.listeners[id] = fn

	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.listeners, id)
	}
}

// CompiledTargetOption configures a CompiledTarget.
type CompiledTargetOption func(*CompiledTarget)

// Compiler sets the function that compiles a Row into a domain object.
func Compiler(fn func(laredo.Row) (any, error)) CompiledTargetOption {
	return func(t *CompiledTarget) {
		t.compiler = fn
	}
}

// KeyFields sets the fields used to extract the composite key from rows.
func KeyFields(fields ...string) CompiledTargetOption {
	return func(t *CompiledTarget) {
		t.keyFields = fields
	}
}

// CompiledFilter sets an optional filter predicate. Rows that don't pass are skipped.
func CompiledFilter(fn func(laredo.Row) bool) CompiledTargetOption {
	return func(t *CompiledTarget) {
		t.filter = fn
	}
}

// compiledEntry holds the compiled domain object alongside the original row
// for snapshot export.
type compiledEntry struct {
	compiled any
	row      laredo.Row
}

// CompiledTarget implements laredo.SyncTarget by deserializing rows into
// strongly-typed domain objects via a pluggable compiler function.
//
// All write operations (OnBaselineRow, OnInsert, OnUpdate, OnDelete, OnTruncate)
// acquire the write lock. Read operations (Get, All, Count) acquire the
// read lock. Listener callbacks run under the write lock and must not block.
type CompiledTarget struct {
	mu sync.RWMutex

	// Configuration (set before OnInit).
	compiler  func(laredo.Row) (any, error)
	keyFields []string
	filter    func(laredo.Row) bool

	// Schema (set during OnInit).
	table   laredo.TableIdentifier
	columns []laredo.ColumnDefinition

	// Primary store: composite key string -> compiledEntry.
	store map[string]compiledEntry

	// Change listeners.
	listeners map[int]func(old, new any)
	nextID    int
}

// NewCompiledTarget creates a new compiled in-memory target.
func NewCompiledTarget(opts ...CompiledTargetOption) *CompiledTarget {
	t := &CompiledTarget{
		listeners: make(map[int]func(old, new any)),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

var _ laredo.SyncTarget = (*CompiledTarget)(nil)

// notifyCompiledListeners calls all registered listeners with the old and new compiled objects.
// Must be called under the write lock.
func (t *CompiledTarget) notifyCompiledListeners(old, new any) {
	for _, fn := range t.listeners {
		fn(old, new)
	}
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnInit(_ context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.table = table
	t.columns = columns

	// Build column name set for validation.
	colSet := make(map[string]struct{}, len(columns))
	for _, c := range columns {
		colSet[c.Name] = struct{}{}
	}

	// Validate key fields exist in columns.
	for _, f := range t.keyFields {
		if _, ok := colSet[f]; !ok {
			return fmt.Errorf("key field %q not found in columns", f)
		}
	}

	// Allocate store.
	t.store = make(map[string]compiledEntry)

	return nil
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnBaselineRow(_ context.Context, _ laredo.TableIdentifier, row laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.insertRow(row)
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnBaselineComplete(_ context.Context, _ laredo.TableIdentifier) error {
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnInsert(_ context.Context, _ laredo.TableIdentifier, columns laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.insertRow(columns)
}

// insertRow compiles and stores a row. Must be called under the write lock.
func (t *CompiledTarget) insertRow(row laredo.Row) error {
	if t.filter != nil && !t.filter(row) {
		return nil
	}

	compiled, err := t.compiler(row)
	if err != nil {
		return nil //nolint:nilerr // compiler errors skip the row
	}

	key := buildKey(row, t.keyFields)
	t.store[key] = compiledEntry{compiled: compiled, row: copyRow(row)}
	t.notifyCompiledListeners(nil, compiled)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnUpdate(_ context.Context, _ laredo.TableIdentifier, columns laredo.Row, identity laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Find old entry by identity.
	oldKey := buildKey(identity, t.keyFields)
	oldEntry, exists := t.store[oldKey]

	// If filter set and new row doesn't pass, treat as delete.
	if t.filter != nil && !t.filter(columns) {
		if exists {
			delete(t.store, oldKey)
			t.notifyCompiledListeners(oldEntry.compiled, nil)
		}
		return nil
	}

	// Compile the new row.
	compiled, err := t.compiler(columns)
	if err != nil {
		return nil //nolint:nilerr // compiler errors skip the row
	}

	// Remove old entry if it existed.
	if exists {
		delete(t.store, oldKey)
	}

	// Store new entry.
	newKey := buildKey(columns, t.keyFields)
	t.store[newKey] = compiledEntry{compiled: compiled, row: copyRow(columns)}

	var oldCompiled any
	if exists {
		oldCompiled = oldEntry.compiled
	}
	t.notifyCompiledListeners(oldCompiled, compiled)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnDelete(_ context.Context, _ laredo.TableIdentifier, identity laredo.Row) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := buildKey(identity, t.keyFields)
	oldEntry, exists := t.store[key]
	if exists {
		delete(t.store, key)
		t.notifyCompiledListeners(oldEntry.compiled, nil)
	}
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnTruncate(_ context.Context, _ laredo.TableIdentifier) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	clear(t.store)
	t.notifyCompiledListeners(nil, nil)
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) IsDurable() bool { return true }

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnSchemaChange(_ context.Context, _ laredo.TableIdentifier, _, _ []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	return laredo.SchemaChangeResponse{Action: laredo.SchemaReBaseline}
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) ExportSnapshot(_ context.Context) ([]laredo.SnapshotEntry, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	entries := make([]laredo.SnapshotEntry, 0, len(t.store))
	for _, entry := range t.store {
		entries = append(entries, laredo.SnapshotEntry{Row: copyRow(entry.row)})
	}
	return entries, nil
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) RestoreSnapshot(_ context.Context, _ laredo.TableSnapshotInfo, entries []laredo.SnapshotEntry) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Clear existing data.
	clear(t.store)

	// Re-compile and store each entry.
	for _, entry := range entries {
		if t.filter != nil && !t.filter(entry.Row) {
			continue
		}

		compiled, err := t.compiler(entry.Row)
		if err != nil {
			continue // skip rows that fail compilation
		}

		key := buildKey(entry.Row, t.keyFields)
		t.store[key] = compiledEntry{compiled: compiled, row: entry.Row}
	}
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) SupportsConsistentSnapshot() bool { return true }

//nolint:revive // implements SyncTarget.
func (t *CompiledTarget) OnClose(_ context.Context, _ laredo.TableIdentifier) error {
	return nil
}

// Get returns the compiled object for the given key field values, or false if not found.
func (t *CompiledTarget) Get(keyValues ...laredo.Value) (any, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	key := buildKeyFromValues(keyValues)
	entry, ok := t.store[key]
	if !ok {
		return nil, false
	}
	return entry.compiled, true
}

// All returns an iterator over all (key, compiled) pairs in the store.
func (t *CompiledTarget) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		t.mu.RLock()
		defer t.mu.RUnlock()

		for k, v := range t.store {
			if !yield(k, v.compiled) {
				return
			}
		}
	}
}

// Count returns the number of entries in the store.
func (t *CompiledTarget) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.store)
}

// Listen registers a change listener that is called for every insert, update,
// delete, and truncate. For inserts, old is nil. For deletes, new is nil.
// For truncate, both are nil. The callback runs under the write lock and must
// not block. Returns an unsubscribe function.
func (t *CompiledTarget) Listen(fn func(old, new any)) func() {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := t.nextID
	t.nextID++
	t.listeners[id] = fn

	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.listeners, id)
	}
}
