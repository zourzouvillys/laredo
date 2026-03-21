package memory_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

const (
	nameAlice   = "alice"
	nameBob     = "bob"
	nameCharlie = "charlie"
	nameAlicia  = "alicia"
	colName     = "name"
	idxByName   = "by_name"

	// Compiled target test constants.
	compiled1Alice   = "1:alice"
	compiled2Bob     = "2:bob"
	compiled3Charlie = "3:charlie"
	compiled1Alicia  = "1:alicia"
)

func initTarget(t *testing.T, target *memory.IndexedTarget) {
	t.Helper()
	err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	if err != nil {
		t.Fatalf("OnInit failed: %v", err)
	}
}

func addBaselineRow(t *testing.T, target *memory.IndexedTarget, id int, name string) {
	t.Helper()
	row := testutil.SampleRow(id, name)
	err := target.OnBaselineRow(context.Background(), testutil.SampleTable(), row)
	if err != nil {
		t.Fatalf("OnBaselineRow failed: %v", err)
	}
}

func TestIndexedTarget_OnInit(t *testing.T) {
	t.Run("basic init", func(t *testing.T) {
		target := memory.NewIndexedTarget()
		err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
		if err != nil {
			t.Fatalf("OnInit failed: %v", err)
		}
	})

	t.Run("with lookup fields", func(t *testing.T) {
		target := memory.NewIndexedTarget(memory.LookupFields(colName))
		err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
		if err != nil {
			t.Fatalf("OnInit failed: %v", err)
		}
	})

	t.Run("invalid lookup field", func(t *testing.T) {
		target := memory.NewIndexedTarget(memory.LookupFields("nonexistent"))
		err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
		if err == nil {
			t.Fatal("expected error for invalid lookup field")
		}
	})

	t.Run("invalid index field", func(t *testing.T) {
		target := memory.NewIndexedTarget(memory.AddIndex(laredo.IndexDefinition{
			Name:   "bad_idx",
			Fields: []string{"nonexistent"},
			Unique: true,
		}))
		err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
		if err == nil {
			t.Fatal("expected error for invalid index field")
		}
	})
}

func TestIndexedTarget_BaselineAndGet(t *testing.T) {
	target := memory.NewIndexedTarget()
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)
	addBaselineRow(t, target, 2, nameBob)

	err := target.OnBaselineComplete(context.Background(), testutil.SampleTable())
	if err != nil {
		t.Fatalf("OnBaselineComplete failed: %v", err)
	}

	// Get by PK.
	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected to find row with id=1")
	}
	if row.GetString(colName) != nameAlice {
		t.Fatalf("expected name=alice, got %v", row.GetString(colName))
	}

	row, ok = target.Get(2)
	if !ok {
		t.Fatal("expected to find row with id=2")
	}
	if row.GetString(colName) != nameBob {
		t.Fatalf("expected name=bob, got %v", row.GetString(colName))
	}

	// Not found.
	_, ok = target.Get(99)
	if ok {
		t.Fatal("expected row with id=99 to not exist")
	}
}

func TestIndexedTarget_Lookup(t *testing.T) {
	target := memory.NewIndexedTarget(memory.LookupFields(colName))
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)
	addBaselineRow(t, target, 2, nameBob)

	row, ok := target.Lookup(nameAlice)
	if !ok {
		t.Fatal("expected to find row with name=alice")
	}
	if row["id"] != 1 {
		t.Fatalf("expected id=1, got %v", row["id"])
	}

	row, ok = target.Lookup(nameBob)
	if !ok {
		t.Fatal("expected to find row with name=bob")
	}
	if row["id"] != 2 {
		t.Fatalf("expected id=2, got %v", row["id"])
	}

	_, ok = target.Lookup(nameCharlie)
	if ok {
		t.Fatal("expected lookup for charlie to fail")
	}

	// Lookup without configured fields returns nothing.
	target2 := memory.NewIndexedTarget()
	initTarget(t, target2)
	_, ok = target2.Lookup("anything")
	if ok {
		t.Fatal("expected lookup to fail when no lookup fields configured")
	}
}

func TestIndexedTarget_SecondaryIndex(t *testing.T) {
	t.Run("unique index", func(t *testing.T) {
		target := memory.NewIndexedTarget(memory.AddIndex(laredo.IndexDefinition{
			Name:   idxByName,
			Fields: []string{colName},
			Unique: true,
		}))
		initTarget(t, target)

		addBaselineRow(t, target, 1, nameAlice)
		addBaselineRow(t, target, 2, nameBob)

		rows := target.LookupAll(idxByName, nameAlice)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		if rows[0]["id"] != 1 {
			t.Fatalf("expected id=1, got %v", rows[0]["id"])
		}

		rows = target.LookupAll(idxByName, nameCharlie)
		if len(rows) != 0 {
			t.Fatalf("expected 0 rows, got %d", len(rows))
		}
	})

	t.Run("non-unique index", func(t *testing.T) {
		target := memory.NewIndexedTarget(memory.AddIndex(laredo.IndexDefinition{
			Name:   idxByName,
			Fields: []string{colName},
			Unique: false,
		}))
		initTarget(t, target)

		addBaselineRow(t, target, 1, nameAlice)
		addBaselineRow(t, target, 2, nameAlice)
		addBaselineRow(t, target, 3, nameBob)

		rows := target.LookupAll(idxByName, nameAlice)
		if len(rows) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(rows))
		}

		rows = target.LookupAll(idxByName, nameBob)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
	})

	t.Run("nonexistent index", func(t *testing.T) {
		target := memory.NewIndexedTarget()
		initTarget(t, target)

		rows := target.LookupAll("no_such_index", "anything")
		if rows != nil {
			t.Fatalf("expected nil for nonexistent index, got %v", rows)
		}
	})
}

func TestIndexedTarget_Insert(t *testing.T) {
	target := memory.NewIndexedTarget(
		memory.LookupFields(colName),
		memory.AddIndex(laredo.IndexDefinition{Name: idxByName, Fields: []string{colName}, Unique: true}),
	)
	initTarget(t, target)

	// Baseline with one row.
	addBaselineRow(t, target, 1, nameAlice)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Insert a new row.
	err := target.OnInsert(context.Background(), testutil.SampleTable(), testutil.SampleRow(2, nameBob))
	if err != nil {
		t.Fatalf("OnInsert failed: %v", err)
	}

	if target.Count() != 2 {
		t.Fatalf("expected count=2, got %d", target.Count())
	}

	row, ok := target.Get(2)
	if !ok {
		t.Fatal("expected to find inserted row")
	}
	if row.GetString(colName) != nameBob {
		t.Fatalf("expected name=bob, got %v", row.GetString(colName))
	}

	// Verify lookup index.
	row, ok = target.Lookup(nameBob)
	if !ok {
		t.Fatal("expected lookup to find bob")
	}
	if row["id"] != 2 {
		t.Fatalf("expected id=2, got %v", row["id"])
	}

	// Verify secondary index.
	rows := target.LookupAll(idxByName, nameBob)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from secondary index, got %d", len(rows))
	}
}

func TestIndexedTarget_Update(t *testing.T) {
	target := memory.NewIndexedTarget(
		memory.LookupFields(colName),
		memory.AddIndex(laredo.IndexDefinition{Name: idxByName, Fields: []string{colName}, Unique: true}),
	)
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)
	addBaselineRow(t, target, 2, nameBob)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Update alice -> alicia.
	identity := laredo.Row{"id": 1}
	newRow := testutil.SampleRow(1, nameAlicia)
	err := target.OnUpdate(context.Background(), testutil.SampleTable(), newRow, identity)
	if err != nil {
		t.Fatalf("OnUpdate failed: %v", err)
	}

	// Count unchanged.
	if target.Count() != 2 {
		t.Fatalf("expected count=2, got %d", target.Count())
	}

	// Old lookup removed.
	_, ok := target.Lookup(nameAlice)
	if ok {
		t.Fatal("expected alice lookup to be removed after update")
	}

	// New lookup works.
	row, ok := target.Lookup(nameAlicia)
	if !ok {
		t.Fatal("expected alicia lookup to work after update")
	}
	if row["id"] != 1 {
		t.Fatalf("expected id=1, got %v", row["id"])
	}

	// Old secondary index entry removed.
	rows := target.LookupAll(idxByName, nameAlice)
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows from old index, got %d", len(rows))
	}

	// New secondary index entry present.
	rows = target.LookupAll(idxByName, nameAlicia)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from new index, got %d", len(rows))
	}

	// PK lookup works.
	row, ok = target.Get(1)
	if !ok {
		t.Fatal("expected to find row by PK after update")
	}
	if row.GetString(colName) != nameAlicia {
		t.Fatalf("expected name=alicia, got %v", row.GetString(colName))
	}
}

func TestIndexedTarget_Delete(t *testing.T) {
	target := memory.NewIndexedTarget(
		memory.LookupFields(colName),
		memory.AddIndex(laredo.IndexDefinition{Name: idxByName, Fields: []string{colName}, Unique: true}),
	)
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)
	addBaselineRow(t, target, 2, nameBob)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Delete alice.
	err := target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 1, colName: nameAlice})
	if err != nil {
		t.Fatalf("OnDelete failed: %v", err)
	}

	if target.Count() != 1 {
		t.Fatalf("expected count=1, got %d", target.Count())
	}

	// PK lookup gone.
	_, ok := target.Get(1)
	if ok {
		t.Fatal("expected row 1 to be deleted")
	}

	// Lookup gone.
	_, ok = target.Lookup(nameAlice)
	if ok {
		t.Fatal("expected alice lookup to be gone after delete")
	}

	// Secondary index gone.
	rows := target.LookupAll(idxByName, nameAlice)
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows from index after delete, got %d", len(rows))
	}

	// Bob still there.
	row, ok := target.Get(2)
	if !ok {
		t.Fatal("expected bob to still exist")
	}
	if row.GetString(colName) != nameBob {
		t.Fatalf("expected name=bob, got %v", row.GetString(colName))
	}

	// Delete nonexistent row: no error.
	err = target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 99})
	if err != nil {
		t.Fatalf("OnDelete of nonexistent row should not error: %v", err)
	}
}

func TestIndexedTarget_Truncate(t *testing.T) {
	target := memory.NewIndexedTarget(
		memory.LookupFields(colName),
		memory.AddIndex(laredo.IndexDefinition{Name: idxByName, Fields: []string{colName}, Unique: false}),
	)
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)
	addBaselineRow(t, target, 2, nameBob)
	addBaselineRow(t, target, 3, nameAlice)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	if target.Count() != 3 {
		t.Fatalf("expected count=3 before truncate, got %d", target.Count())
	}

	err := target.OnTruncate(context.Background(), testutil.SampleTable())
	if err != nil {
		t.Fatalf("OnTruncate failed: %v", err)
	}

	if target.Count() != 0 {
		t.Fatalf("expected count=0 after truncate, got %d", target.Count())
	}

	_, ok := target.Get(1)
	if ok {
		t.Fatal("expected no rows after truncate")
	}

	_, ok = target.Lookup(nameAlice)
	if ok {
		t.Fatal("expected lookup to fail after truncate")
	}

	rows := target.LookupAll(idxByName, nameAlice)
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows from index after truncate, got %d", len(rows))
	}
}

func TestIndexedTarget_Count(t *testing.T) {
	target := memory.NewIndexedTarget()
	initTarget(t, target)

	if target.Count() != 0 {
		t.Fatalf("expected count=0 initially, got %d", target.Count())
	}

	addBaselineRow(t, target, 1, nameAlice)
	if target.Count() != 1 {
		t.Fatalf("expected count=1, got %d", target.Count())
	}

	addBaselineRow(t, target, 2, nameBob)
	if target.Count() != 2 {
		t.Fatalf("expected count=2, got %d", target.Count())
	}

	_ = target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 1})
	if target.Count() != 1 {
		t.Fatalf("expected count=1 after delete, got %d", target.Count())
	}

	_ = target.OnTruncate(context.Background(), testutil.SampleTable())
	if target.Count() != 0 {
		t.Fatalf("expected count=0 after truncate, got %d", target.Count())
	}
}

func TestIndexedTarget_All(t *testing.T) {
	target := memory.NewIndexedTarget()
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)
	addBaselineRow(t, target, 2, nameBob)
	addBaselineRow(t, target, 3, nameCharlie)

	collected := make(map[string]laredo.Row)
	for k, v := range target.All() {
		collected[k] = v
	}

	if len(collected) != 3 {
		t.Fatalf("expected 3 rows from All(), got %d", len(collected))
	}

	// Verify we got all names.
	names := make(map[string]bool)
	for _, row := range collected {
		names[row.GetString(colName)] = true
	}
	for _, expected := range []string{nameAlice, nameBob, nameCharlie} {
		if !names[expected] {
			t.Fatalf("expected name=%s in All() results", expected)
		}
	}
}

func TestIndexedTarget_Listen(t *testing.T) {
	target := memory.NewIndexedTarget()
	initTarget(t, target)

	type notification struct {
		old laredo.Row
		new laredo.Row
	}
	var notifications []notification

	target.Listen(func(old, new laredo.Row) {
		notifications = append(notifications, notification{old: old, new: new})
	})

	// Insert.
	addBaselineRow(t, target, 1, nameAlice)
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].old != nil {
		t.Fatal("expected old=nil for insert")
	}
	if notifications[0].new.GetString(colName) != nameAlice {
		t.Fatalf("expected new.name=alice, got %v", notifications[0].new.GetString(colName))
	}

	// Update.
	_ = target.OnUpdate(context.Background(), testutil.SampleTable(), testutil.SampleRow(1, nameAlicia), laredo.Row{"id": 1})
	if len(notifications) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(notifications))
	}
	if notifications[1].old == nil {
		t.Fatal("expected old!=nil for update")
	}
	if notifications[1].old.GetString(colName) != nameAlice {
		t.Fatalf("expected old.name=alice, got %v", notifications[1].old.GetString(colName))
	}
	if notifications[1].new.GetString(colName) != nameAlicia {
		t.Fatalf("expected new.name=alicia, got %v", notifications[1].new.GetString(colName))
	}

	// Delete.
	_ = target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 1})
	if len(notifications) != 3 {
		t.Fatalf("expected 3 notifications, got %d", len(notifications))
	}
	if notifications[2].old == nil {
		t.Fatal("expected old!=nil for delete")
	}
	if notifications[2].new != nil {
		t.Fatal("expected new=nil for delete")
	}

	// Truncate.
	addBaselineRow(t, target, 2, nameBob)
	_ = target.OnTruncate(context.Background(), testutil.SampleTable())
	if len(notifications) != 5 { // +1 for baseline insert, +1 for truncate
		t.Fatalf("expected 5 notifications, got %d", len(notifications))
	}
	// Truncate notification: both nil.
	if notifications[4].old != nil || notifications[4].new != nil {
		t.Fatal("expected both old and new to be nil for truncate")
	}
}

func TestIndexedTarget_ListenUnsubscribe(t *testing.T) {
	target := memory.NewIndexedTarget()
	initTarget(t, target)

	callCount := 0
	unsub := target.Listen(func(_, _ laredo.Row) {
		callCount++
	})

	addBaselineRow(t, target, 1, nameAlice)
	if callCount != 1 {
		t.Fatalf("expected 1 call, got %d", callCount)
	}

	unsub()

	addBaselineRow(t, target, 2, nameBob)
	if callCount != 1 {
		t.Fatalf("expected still 1 call after unsubscribe, got %d", callCount)
	}
}

func TestIndexedTarget_ExportRestoreSnapshot(t *testing.T) {
	target := memory.NewIndexedTarget(
		memory.LookupFields(colName),
		memory.AddIndex(laredo.IndexDefinition{Name: idxByName, Fields: []string{colName}, Unique: true}),
	)
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)
	addBaselineRow(t, target, 2, nameBob)
	addBaselineRow(t, target, 3, nameCharlie)

	// Export.
	entries, err := target.ExportSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ExportSnapshot failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Verify SupportsConsistentSnapshot.
	if !target.SupportsConsistentSnapshot() {
		t.Fatal("expected SupportsConsistentSnapshot=true")
	}

	// Clear and restore.
	err = target.RestoreSnapshot(context.Background(), laredo.TableSnapshotInfo{}, entries)
	if err != nil {
		t.Fatalf("RestoreSnapshot failed: %v", err)
	}

	// Verify data integrity.
	if target.Count() != 3 {
		t.Fatalf("expected count=3 after restore, got %d", target.Count())
	}

	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected row with id=1 after restore")
	}
	if row.GetString(colName) != nameAlice {
		t.Fatalf("expected name=alice, got %v", row.GetString(colName))
	}

	// Lookup index should work after restore.
	row, ok = target.Lookup(nameBob)
	if !ok {
		t.Fatal("expected lookup for bob to work after restore")
	}
	if row["id"] != 2 {
		t.Fatalf("expected id=2, got %v", row["id"])
	}

	// Secondary index should work after restore.
	rows := target.LookupAll(idxByName, nameCharlie)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from index after restore, got %d", len(rows))
	}
}

func TestIndexedTarget_ConcurrentReads(t *testing.T) {
	target := memory.NewIndexedTarget(memory.LookupFields(colName))
	initTarget(t, target)

	// Pre-populate.
	for i := range 100 {
		addBaselineRow(t, target, i, colName)
	}

	var wg sync.WaitGroup
	ctx := context.Background()

	// Concurrent readers.
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = target.Count()
				target.Get(50)
				target.Lookup(colName)
				for range target.All() {
					break // just iterate one
				}
			}
		}()
	}

	// Concurrent writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 100; i < 200; i++ {
			_ = target.OnInsert(ctx, testutil.SampleTable(), testutil.SampleRow(i, colName))
		}
	}()

	wg.Wait()

	// Should have 200 rows total.
	if target.Count() != 200 {
		t.Fatalf("expected count=200, got %d", target.Count())
	}
}

func TestIndexedTarget_OnSchemaChange(t *testing.T) {
	t.Run("new column added returns CONTINUE", func(t *testing.T) {
		target := memory.NewIndexedTarget()
		initTarget(t, target)

		oldCols := testutil.SampleColumns()
		newCols := append(sliceClone(oldCols), laredo.ColumnDefinition{
			Name: "email", Type: "text", Nullable: true,
		})

		resp := target.OnSchemaChange(context.Background(), testutil.SampleTable(), oldCols, newCols)
		if resp.Action != laredo.SchemaContinue {
			t.Fatalf("expected SchemaContinue, got %v", resp.Action)
		}
	})

	t.Run("column dropped returns RE_BASELINE", func(t *testing.T) {
		target := memory.NewIndexedTarget()
		initTarget(t, target)

		oldCols := testutil.SampleColumns()
		// Drop the "value" column.
		newCols := oldCols[:2]

		resp := target.OnSchemaChange(context.Background(), testutil.SampleTable(), oldCols, newCols)
		if resp.Action != laredo.SchemaReBaseline {
			t.Fatalf("expected SchemaReBaseline, got %v", resp.Action)
		}
	})

	t.Run("same columns returns RE_BASELINE", func(t *testing.T) {
		target := memory.NewIndexedTarget()
		initTarget(t, target)

		cols := testutil.SampleColumns()
		resp := target.OnSchemaChange(context.Background(), testutil.SampleTable(), cols, cols)
		if resp.Action != laredo.SchemaReBaseline {
			t.Fatalf("expected SchemaReBaseline for same columns, got %v", resp.Action)
		}
	})
}

func TestIndexedTarget_IsDurable(t *testing.T) {
	target := memory.NewIndexedTarget()
	if !target.IsDurable() {
		t.Fatal("expected IsDurable=true")
	}
}

func TestIndexedTarget_OnClose(t *testing.T) {
	target := memory.NewIndexedTarget()
	initTarget(t, target)

	err := target.OnClose(context.Background(), testutil.SampleTable())
	if err != nil {
		t.Fatalf("OnClose failed: %v", err)
	}
}

func TestIndexedTarget_NonUniqueIndexDeleteMaintenance(t *testing.T) {
	// Verifies that deleting one row from a non-unique index doesn't remove others.
	target := memory.NewIndexedTarget(memory.AddIndex(laredo.IndexDefinition{
		Name:   idxByName,
		Fields: []string{colName},
		Unique: false,
	}))
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)
	addBaselineRow(t, target, 2, nameAlice)
	addBaselineRow(t, target, 3, nameAlice)

	// Delete row 2.
	_ = target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 2, colName: nameAlice})

	rows := target.LookupAll(idxByName, nameAlice)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows after deleting one, got %d", len(rows))
	}

	// Verify the remaining rows are 1 and 3.
	ids := make(map[any]bool)
	for _, r := range rows {
		ids[r["id"]] = true
	}
	if !ids[1] || !ids[3] {
		t.Fatalf("expected ids 1 and 3 to remain, got %v", ids)
	}
}

func TestIndexedTarget_RowCopyProtection(t *testing.T) {
	target := memory.NewIndexedTarget()
	initTarget(t, target)

	addBaselineRow(t, target, 1, nameAlice)

	// Get a row and mutate it.
	row, ok := target.Get(1)
	if !ok {
		t.Fatal("expected to find row")
	}
	row[colName] = "MUTATED"

	// Original should be unchanged.
	row2, ok := target.Get(1)
	if !ok {
		t.Fatal("expected to find row")
	}
	if row2.GetString(colName) != nameAlice {
		t.Fatalf("expected name=alice (immutable), got %v", row2.GetString(colName))
	}
}

// sliceClone returns a shallow copy of a slice.
func sliceClone[T any](s []T) []T {
	out := make([]T, len(s))
	copy(out, s)
	return out
}

// --- CompiledTarget tests ---

// testCompiler is a simple compiler that produces "id:name" strings.
func testCompiler(row laredo.Row) (any, error) {
	return fmt.Sprintf("%v:%v", row["id"], row["name"]), nil
}

func initCompiledTarget(t *testing.T, target *memory.CompiledTarget) {
	t.Helper()
	err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	if err != nil {
		t.Fatalf("OnInit failed: %v", err)
	}
}

func addCompiledBaselineRow(t *testing.T, target *memory.CompiledTarget, id int, name string) {
	t.Helper()
	row := testutil.SampleRow(id, name)
	err := target.OnBaselineRow(context.Background(), testutil.SampleTable(), row)
	if err != nil {
		t.Fatalf("OnBaselineRow failed: %v", err)
	}
}

func TestCompiledTarget_OnInit(t *testing.T) {
	t.Run("basic init", func(t *testing.T) {
		target := memory.NewCompiledTarget(
			memory.Compiler(testCompiler),
			memory.KeyFields("id"),
		)
		err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
		if err != nil {
			t.Fatalf("OnInit failed: %v", err)
		}
	})

	t.Run("invalid key field", func(t *testing.T) {
		target := memory.NewCompiledTarget(
			memory.Compiler(testCompiler),
			memory.KeyFields("nonexistent"),
		)
		err := target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
		if err == nil {
			t.Fatal("expected error for invalid key field")
		}
	})
}

func TestCompiledTarget_BaselineAndGet(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	addCompiledBaselineRow(t, target, 2, nameBob)

	err := target.OnBaselineComplete(context.Background(), testutil.SampleTable())
	if err != nil {
		t.Fatalf("OnBaselineComplete failed: %v", err)
	}

	// Get by key.
	val, ok := target.Get(1)
	if !ok {
		t.Fatal("expected to find entry with id=1")
	}
	if val != compiled1Alice {
		t.Fatalf("expected %s, got %v", compiled1Alice, val)
	}

	val, ok = target.Get(2)
	if !ok {
		t.Fatal("expected to find entry with id=2")
	}
	if val != compiled2Bob {
		t.Fatalf("expected %s, got %v", compiled2Bob, val)
	}

	// Not found.
	_, ok = target.Get(99)
	if ok {
		t.Fatal("expected entry with id=99 to not exist")
	}
}

func TestCompiledTarget_Filter(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
		memory.CompiledFilter(func(row laredo.Row) bool {
			// Only accept rows where name is not "bob".
			return row.GetString(colName) != nameBob
		}),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	addCompiledBaselineRow(t, target, 2, nameBob)
	addCompiledBaselineRow(t, target, 3, nameCharlie)

	if target.Count() != 2 {
		t.Fatalf("expected count=2 (bob filtered), got %d", target.Count())
	}

	_, ok := target.Get(1)
	if !ok {
		t.Fatal("expected alice to be stored")
	}

	_, ok = target.Get(2)
	if ok {
		t.Fatal("expected bob to be filtered out")
	}

	_, ok = target.Get(3)
	if !ok {
		t.Fatal("expected charlie to be stored")
	}
}

func TestCompiledTarget_Insert(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Insert a new row.
	err := target.OnInsert(context.Background(), testutil.SampleTable(), testutil.SampleRow(2, nameBob))
	if err != nil {
		t.Fatalf("OnInsert failed: %v", err)
	}

	if target.Count() != 2 {
		t.Fatalf("expected count=2, got %d", target.Count())
	}

	val, ok := target.Get(2)
	if !ok {
		t.Fatal("expected to find inserted entry")
	}
	if val != compiled2Bob {
		t.Fatalf("expected %s, got %v", compiled2Bob, val)
	}
}

func TestCompiledTarget_Update(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	addCompiledBaselineRow(t, target, 2, nameBob)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Update alice -> alicia.
	identity := laredo.Row{"id": 1}
	newRow := testutil.SampleRow(1, nameAlicia)
	err := target.OnUpdate(context.Background(), testutil.SampleTable(), newRow, identity)
	if err != nil {
		t.Fatalf("OnUpdate failed: %v", err)
	}

	if target.Count() != 2 {
		t.Fatalf("expected count=2, got %d", target.Count())
	}

	val, ok := target.Get(1)
	if !ok {
		t.Fatal("expected to find updated entry")
	}
	if val != compiled1Alicia {
		t.Fatalf("expected %s, got %v", compiled1Alicia, val)
	}
}

func TestCompiledTarget_UpdateFilterRemoves(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
		memory.CompiledFilter(func(row laredo.Row) bool {
			return row.GetString(colName) != nameBob
		}),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	if target.Count() != 1 {
		t.Fatalf("expected count=1, got %d", target.Count())
	}

	// Update alice -> bob (fails filter, treated as delete).
	identity := laredo.Row{"id": 1}
	newRow := testutil.SampleRow(1, nameBob)
	err := target.OnUpdate(context.Background(), testutil.SampleTable(), newRow, identity)
	if err != nil {
		t.Fatalf("OnUpdate failed: %v", err)
	}

	if target.Count() != 0 {
		t.Fatalf("expected count=0 after filter-remove, got %d", target.Count())
	}

	_, ok := target.Get(1)
	if ok {
		t.Fatal("expected entry to be removed by filter")
	}
}

func TestCompiledTarget_Delete(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	addCompiledBaselineRow(t, target, 2, nameBob)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Delete alice.
	err := target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 1})
	if err != nil {
		t.Fatalf("OnDelete failed: %v", err)
	}

	if target.Count() != 1 {
		t.Fatalf("expected count=1, got %d", target.Count())
	}

	_, ok := target.Get(1)
	if ok {
		t.Fatal("expected alice to be deleted")
	}

	// Bob still there.
	val, ok := target.Get(2)
	if !ok {
		t.Fatal("expected bob to still exist")
	}
	if val != compiled2Bob {
		t.Fatalf("expected %s, got %v", compiled2Bob, val)
	}

	// Delete nonexistent row: no error.
	err = target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 99})
	if err != nil {
		t.Fatalf("OnDelete of nonexistent row should not error: %v", err)
	}
}

func TestCompiledTarget_Truncate(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	addCompiledBaselineRow(t, target, 2, nameBob)
	addCompiledBaselineRow(t, target, 3, nameCharlie)
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	if target.Count() != 3 {
		t.Fatalf("expected count=3 before truncate, got %d", target.Count())
	}

	err := target.OnTruncate(context.Background(), testutil.SampleTable())
	if err != nil {
		t.Fatalf("OnTruncate failed: %v", err)
	}

	if target.Count() != 0 {
		t.Fatalf("expected count=0 after truncate, got %d", target.Count())
	}

	_, ok := target.Get(1)
	if ok {
		t.Fatal("expected no entries after truncate")
	}
}

func TestCompiledTarget_Count(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	if target.Count() != 0 {
		t.Fatalf("expected count=0 initially, got %d", target.Count())
	}

	addCompiledBaselineRow(t, target, 1, nameAlice)
	if target.Count() != 1 {
		t.Fatalf("expected count=1, got %d", target.Count())
	}

	addCompiledBaselineRow(t, target, 2, nameBob)
	if target.Count() != 2 {
		t.Fatalf("expected count=2, got %d", target.Count())
	}

	_ = target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 1})
	if target.Count() != 1 {
		t.Fatalf("expected count=1 after delete, got %d", target.Count())
	}

	_ = target.OnTruncate(context.Background(), testutil.SampleTable())
	if target.Count() != 0 {
		t.Fatalf("expected count=0 after truncate, got %d", target.Count())
	}
}

func TestCompiledTarget_All(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	addCompiledBaselineRow(t, target, 2, nameBob)
	addCompiledBaselineRow(t, target, 3, nameCharlie)

	collected := make(map[string]any)
	for k, v := range target.All() {
		collected[k] = v
	}

	if len(collected) != 3 {
		t.Fatalf("expected 3 entries from All(), got %d", len(collected))
	}

	// Verify we got all compiled values.
	values := make(map[string]bool)
	for _, v := range collected {
		values[v.(string)] = true
	}
	for _, expected := range []string{compiled1Alice, compiled2Bob, compiled3Charlie} {
		if !values[expected] {
			t.Fatalf("expected value=%s in All() results", expected)
		}
	}
}

func TestCompiledTarget_Listen(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	type notification struct {
		old any
		new any
	}
	var notifications []notification

	unsub := target.Listen(func(old, new any) {
		notifications = append(notifications, notification{old: old, new: new})
	})

	// Insert.
	addCompiledBaselineRow(t, target, 1, nameAlice)
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].old != nil {
		t.Fatal("expected old=nil for insert")
	}
	if notifications[0].new != compiled1Alice {
		t.Fatalf("expected new=%s, got %v", compiled1Alice, notifications[0].new)
	}

	// Update.
	_ = target.OnUpdate(context.Background(), testutil.SampleTable(), testutil.SampleRow(1, nameAlicia), laredo.Row{"id": 1})
	if len(notifications) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(notifications))
	}
	if notifications[1].old != compiled1Alice {
		t.Fatalf("expected old=%s, got %v", compiled1Alice, notifications[1].old)
	}
	if notifications[1].new != compiled1Alicia {
		t.Fatalf("expected new=%s, got %v", compiled1Alicia, notifications[1].new)
	}

	// Delete.
	_ = target.OnDelete(context.Background(), testutil.SampleTable(), laredo.Row{"id": 1})
	if len(notifications) != 3 {
		t.Fatalf("expected 3 notifications, got %d", len(notifications))
	}
	if notifications[2].old != compiled1Alicia {
		t.Fatalf("expected old=%s, got %v", compiled1Alicia, notifications[2].old)
	}
	if notifications[2].new != nil {
		t.Fatal("expected new=nil for delete")
	}

	// Truncate.
	addCompiledBaselineRow(t, target, 2, nameBob)
	_ = target.OnTruncate(context.Background(), testutil.SampleTable())
	if len(notifications) != 5 { // +1 for baseline insert, +1 for truncate
		t.Fatalf("expected 5 notifications, got %d", len(notifications))
	}
	// Truncate notification: both nil.
	if notifications[4].old != nil || notifications[4].new != nil {
		t.Fatal("expected both old and new to be nil for truncate")
	}

	// Unsubscribe.
	unsub()
	addCompiledBaselineRow(t, target, 3, nameCharlie)
	if len(notifications) != 5 {
		t.Fatalf("expected still 5 notifications after unsubscribe, got %d", len(notifications))
	}
}

func TestCompiledTarget_ExportRestoreSnapshot(t *testing.T) {
	target := memory.NewCompiledTarget(
		memory.Compiler(testCompiler),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	addCompiledBaselineRow(t, target, 2, nameBob)
	addCompiledBaselineRow(t, target, 3, nameCharlie)

	// Export.
	entries, err := target.ExportSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ExportSnapshot failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Verify SupportsConsistentSnapshot.
	if !target.SupportsConsistentSnapshot() {
		t.Fatal("expected SupportsConsistentSnapshot=true")
	}

	// Clear and restore.
	err = target.RestoreSnapshot(context.Background(), laredo.TableSnapshotInfo{}, entries)
	if err != nil {
		t.Fatalf("RestoreSnapshot failed: %v", err)
	}

	// Verify data integrity.
	if target.Count() != 3 {
		t.Fatalf("expected count=3 after restore, got %d", target.Count())
	}

	val, ok := target.Get(1)
	if !ok {
		t.Fatal("expected entry with id=1 after restore")
	}
	if val != compiled1Alice {
		t.Fatalf("expected %s, got %v", compiled1Alice, val)
	}

	val, ok = target.Get(2)
	if !ok {
		t.Fatal("expected entry with id=2 after restore")
	}
	if val != compiled2Bob {
		t.Fatalf("expected %s, got %v", compiled2Bob, val)
	}

	val, ok = target.Get(3)
	if !ok {
		t.Fatal("expected entry with id=3 after restore")
	}
	if val != compiled3Charlie {
		t.Fatalf("expected %s, got %v", compiled3Charlie, val)
	}
}

func TestCompiledTarget_CompilerError(t *testing.T) {
	callCount := 0
	target := memory.NewCompiledTarget(
		memory.Compiler(func(row laredo.Row) (any, error) {
			callCount++
			if row.GetString(colName) == nameBob {
				return nil, fmt.Errorf("compile error for bob")
			}
			return fmt.Sprintf("%v:%v", row["id"], row["name"]), nil
		}),
		memory.KeyFields("id"),
	)
	initCompiledTarget(t, target)

	addCompiledBaselineRow(t, target, 1, nameAlice)
	addCompiledBaselineRow(t, target, 2, nameBob) // should be skipped
	addCompiledBaselineRow(t, target, 3, nameCharlie)

	// Compiler was called for all 3 rows.
	if callCount != 3 {
		t.Fatalf("expected compiler called 3 times, got %d", callCount)
	}

	// Only 2 rows stored (bob was skipped).
	if target.Count() != 2 {
		t.Fatalf("expected count=2 (bob skipped), got %d", target.Count())
	}

	_, ok := target.Get(1)
	if !ok {
		t.Fatal("expected alice to be stored")
	}

	_, ok = target.Get(2)
	if ok {
		t.Fatal("expected bob to be skipped due to compiler error")
	}

	_, ok = target.Get(3)
	if !ok {
		t.Fatal("expected charlie to be stored")
	}
}
