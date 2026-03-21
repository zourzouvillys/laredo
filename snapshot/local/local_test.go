package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

func testMetadata(snapshotID string, createdAt time.Time, tables ...laredo.TableSnapshotInfo) laredo.SnapshotMetadata {
	return laredo.SnapshotMetadata{
		SnapshotID: snapshotID,
		CreatedAt:  createdAt,
		Format:     "jsonl",
		Tables:     tables,
	}
}

func testTableInfo(schema, table string, columns []laredo.ColumnDefinition, rowCount int64) laredo.TableSnapshotInfo {
	return laredo.TableSnapshotInfo{
		Table:    laredo.Table(schema, table),
		RowCount: rowCount,
		Columns:  columns,
	}
}

func testColumns() []laredo.ColumnDefinition {
	return []laredo.ColumnDefinition{
		{Name: "id", Type: "integer", PrimaryKey: true},
		{Name: "name", Type: "text"},
	}
}

func testEntries(rows ...laredo.Row) []laredo.SnapshotEntry {
	entries := make([]laredo.SnapshotEntry, len(rows))
	for i, r := range rows {
		entries[i] = laredo.SnapshotEntry{Row: r}
	}
	return entries
}

func TestStore_SaveAndLoad(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	table1 := laredo.Table("public", "users")
	table2 := laredo.Table("public", "orders")
	cols := testColumns()

	metadata := testMetadata("snap-1", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		testTableInfo("public", "users", cols, 2),
		testTableInfo("public", "orders", cols, 1),
	)

	entries := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		table1: testEntries(
			laredo.Row{"id": float64(1), "name": "alice"},
			laredo.Row{"id": float64(2), "name": "bob"},
		),
		table2: testEntries(
			laredo.Row{"id": float64(100), "name": "order-a"},
		),
	}

	desc, err := store.Save(ctx, "snap-1", metadata, entries)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if desc.SnapshotID != "snap-1" {
		t.Errorf("SnapshotID = %q, want %q", desc.SnapshotID, "snap-1")
	}
	if desc.Format != "jsonl" {
		t.Errorf("Format = %q, want %q", desc.Format, "jsonl")
	}
	if len(desc.Tables) != 2 {
		t.Errorf("len(Tables) = %d, want 2", len(desc.Tables))
	}
	if desc.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", desc.SizeBytes)
	}

	// Load and verify.
	loadedMeta, loadedEntries, err := store.Load(ctx, "snap-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loadedMeta.SnapshotID != "snap-1" {
		t.Errorf("loaded SnapshotID = %q, want %q", loadedMeta.SnapshotID, "snap-1")
	}
	if !loadedMeta.CreatedAt.Equal(metadata.CreatedAt) {
		t.Errorf("loaded CreatedAt = %v, want %v", loadedMeta.CreatedAt, metadata.CreatedAt)
	}

	// Verify users table.
	userEntries, ok := loadedEntries[table1]
	if !ok {
		t.Fatal("missing users table in loaded entries")
	}
	if len(userEntries) != 2 {
		t.Fatalf("len(users) = %d, want 2", len(userEntries))
	}
	if userEntries[0].Row.GetString("name") != "alice" {
		t.Errorf("users[0].name = %q, want %q", userEntries[0].Row.GetString("name"), "alice")
	}
	if userEntries[1].Row.GetString("name") != "bob" {
		t.Errorf("users[1].name = %q, want %q", userEntries[1].Row.GetString("name"), "bob")
	}

	// Verify orders table.
	orderEntries, ok := loadedEntries[table2]
	if !ok {
		t.Fatal("missing orders table in loaded entries")
	}
	if len(orderEntries) != 1 {
		t.Fatalf("len(orders) = %d, want 1", len(orderEntries))
	}
	if orderEntries[0].Row.GetString("name") != "order-a" {
		t.Errorf("orders[0].name = %q, want %q", orderEntries[0].Row.GetString("name"), "order-a")
	}
}

func TestStore_Describe(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	cols := testColumns()
	table := laredo.Table("public", "items")
	metadata := testMetadata("snap-desc", time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		testTableInfo("public", "items", cols, 3),
	)
	entries := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		table: testEntries(
			laredo.Row{"id": float64(1), "name": "a"},
			laredo.Row{"id": float64(2), "name": "b"},
			laredo.Row{"id": float64(3), "name": "c"},
		),
	}

	if _, err := store.Save(ctx, "snap-desc", metadata, entries); err != nil {
		t.Fatalf("Save: %v", err)
	}

	desc, err := store.Describe(ctx, "snap-desc")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	if desc.SnapshotID != "snap-desc" {
		t.Errorf("SnapshotID = %q, want %q", desc.SnapshotID, "snap-desc")
	}
	if !desc.CreatedAt.Equal(metadata.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", desc.CreatedAt, metadata.CreatedAt)
	}
	if desc.Format != "jsonl" {
		t.Errorf("Format = %q, want %q", desc.Format, "jsonl")
	}
	if len(desc.Tables) != 1 {
		t.Fatalf("len(Tables) = %d, want 1", len(desc.Tables))
	}
	if desc.Tables[0].Table != table {
		t.Errorf("Tables[0].Table = %v, want %v", desc.Tables[0].Table, table)
	}
	if desc.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", desc.SizeBytes)
	}
}

func TestStore_List(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	cols := testColumns()
	table := laredo.Table("public", "data")

	// Save 3 snapshots with different creation times.
	times := []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}

	for i, ts := range times {
		id := "snap-" + string(rune('a'+i))
		meta := testMetadata(id, ts, testTableInfo("public", "data", cols, 1))
		entries := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			table: testEntries(laredo.Row{"id": float64(i)}),
		}
		if _, err := store.Save(ctx, id, meta, entries); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}

	list, err := store.List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(list) != 3 {
		t.Fatalf("len(list) = %d, want 3", len(list))
	}

	// Verify sorted by CreatedAt descending.
	if list[0].SnapshotID != "snap-c" {
		t.Errorf("list[0].SnapshotID = %q, want %q", list[0].SnapshotID, "snap-c")
	}
	if list[1].SnapshotID != "snap-b" {
		t.Errorf("list[1].SnapshotID = %q, want %q", list[1].SnapshotID, "snap-b")
	}
	if list[2].SnapshotID != "snap-a" {
		t.Errorf("list[2].SnapshotID = %q, want %q", list[2].SnapshotID, "snap-a")
	}
}

func TestStore_ListWithFilter(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	cols := testColumns()
	tableUsers := laredo.Table("public", "users")
	tableOrders := laredo.Table("public", "orders")

	// Snapshot with users table only.
	meta1 := testMetadata("snap-users", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		testTableInfo("public", "users", cols, 1),
	)
	entries1 := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		tableUsers: testEntries(laredo.Row{"id": float64(1), "name": "alice"}),
	}
	if _, err := store.Save(ctx, "snap-users", meta1, entries1); err != nil {
		t.Fatalf("Save snap-users: %v", err)
	}

	// Snapshot with orders table only.
	meta2 := testMetadata("snap-orders", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		testTableInfo("public", "orders", cols, 1),
	)
	entries2 := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		tableOrders: testEntries(laredo.Row{"id": float64(100), "name": "order-a"}),
	}
	if _, err := store.Save(ctx, "snap-orders", meta2, entries2); err != nil {
		t.Fatalf("Save snap-orders: %v", err)
	}

	// Snapshot with both tables.
	meta3 := testMetadata("snap-both", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		testTableInfo("public", "users", cols, 1),
		testTableInfo("public", "orders", cols, 1),
	)
	entries3 := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		tableUsers:  testEntries(laredo.Row{"id": float64(2), "name": "bob"}),
		tableOrders: testEntries(laredo.Row{"id": float64(200), "name": "order-b"}),
	}
	if _, err := store.Save(ctx, "snap-both", meta3, entries3); err != nil {
		t.Fatalf("Save snap-both: %v", err)
	}

	// Filter by users table.
	filter := &laredo.SnapshotFilter{Table: &tableUsers}
	list, err := store.List(ctx, filter)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}

	// Should have snap-both (newest) and snap-users.
	ids := make([]string, len(list))
	for i, d := range list {
		ids[i] = d.SnapshotID
	}
	if ids[0] != "snap-both" || ids[1] != "snap-users" {
		t.Errorf("filtered list = %v, want [snap-both, snap-users]", ids)
	}

	// Filter by orders table.
	filter = &laredo.SnapshotFilter{Table: &tableOrders}
	list, err = store.List(ctx, filter)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	ids = make([]string, len(list))
	for i, d := range list {
		ids[i] = d.SnapshotID
	}
	if ids[0] != "snap-both" || ids[1] != "snap-orders" {
		t.Errorf("filtered list = %v, want [snap-both, snap-orders]", ids)
	}
}

func TestStore_Delete(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	cols := testColumns()
	table := laredo.Table("public", "data")
	meta := testMetadata("snap-del", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		testTableInfo("public", "data", cols, 1),
	)
	entries := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		table: testEntries(laredo.Row{"id": float64(1), "name": "x"}),
	}

	if _, err := store.Save(ctx, "snap-del", meta, entries); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify directory exists.
	snapshotDir := filepath.Join(store.basePath, "snap-del")
	if _, err := os.Stat(snapshotDir); err != nil {
		t.Fatalf("snapshot dir should exist: %v", err)
	}

	if err := store.Delete(ctx, "snap-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify directory removed.
	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Errorf("snapshot dir should not exist after delete, err = %v", err)
	}

	// Load should fail.
	_, _, err := store.Load(ctx, "snap-del")
	if err == nil {
		t.Error("Load after delete should return error")
	}
}

func TestStore_Prune(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	cols := testColumns()
	table := laredo.Table("public", "data")

	// Save 5 snapshots.
	for i := range 5 {
		id := "snap-" + string(rune('a'+i))
		ts := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)
		meta := testMetadata(id, ts, testTableInfo("public", "data", cols, 1))
		entries := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
			table: testEntries(laredo.Row{"id": float64(i)}),
		}
		if _, err := store.Save(ctx, id, meta, entries); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}

	// Prune to keep 2.
	if err := store.Prune(ctx, 2, nil); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	list, err := store.List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}

	// The two newest should remain: snap-e and snap-d.
	if list[0].SnapshotID != "snap-e" {
		t.Errorf("list[0].SnapshotID = %q, want %q", list[0].SnapshotID, "snap-e")
	}
	if list[1].SnapshotID != "snap-d" {
		t.Errorf("list[1].SnapshotID = %q, want %q", list[1].SnapshotID, "snap-d")
	}

	// Verify oldest 3 are deleted.
	for _, id := range []string{"snap-a", "snap-b", "snap-c"} {
		dir := filepath.Join(store.basePath, id)
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("snapshot %s should have been pruned", id)
		}
	}
}

func TestStore_SaveAtomicOnError(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	cols := testColumns()
	table := laredo.Table("public", "data")
	meta := testMetadata("snap-atomic", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		testTableInfo("public", "data", cols, 1),
	)
	entries := map[laredo.TableIdentifier][]laredo.SnapshotEntry{
		table: testEntries(laredo.Row{"id": float64(1), "name": "test"}),
	}

	// Successful save.
	if _, err := store.Save(ctx, "snap-atomic", meta, entries); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify final directory exists.
	finalDir := filepath.Join(store.basePath, "snap-atomic")
	if _, err := os.Stat(finalDir); err != nil {
		t.Fatalf("final dir should exist: %v", err)
	}

	// Verify no .tmp-* directories remain.
	dirEntries, err := os.ReadDir(store.basePath)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range dirEntries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Errorf("temp directory %q should not remain after successful save", entry.Name())
		}
	}
}

func TestStore_LoadNonexistent(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()

	_, _, err := store.Load(ctx, "nonexistent")
	if err == nil {
		t.Error("Load nonexistent snapshot should return error")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention snapshot ID, got: %v", err)
	}
}
