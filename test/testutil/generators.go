package testutil

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/zourzouvillys/laredo"
)

// RandomSchema returns a random column definition list with the given number of columns.
// Always includes an "id" primary key column as the first column.
//
//nolint:gosec // math/rand is fine for test data generation.
func RandomSchema(numColumns int) []laredo.ColumnDefinition {
	if numColumns < 1 {
		numColumns = 1
	}
	types := []string{"text", "integer", "boolean", "jsonb", "timestamp", "float8", "uuid"}
	cols := make([]laredo.ColumnDefinition, 0, numColumns)
	cols = append(cols, laredo.ColumnDefinition{
		Name: "id", Type: "integer", Nullable: false, PrimaryKey: true,
	})
	for i := 1; i < numColumns; i++ {
		cols = append(cols, laredo.ColumnDefinition{
			Name:     fmt.Sprintf("col_%d", i),
			Type:     types[rand.IntN(len(types))],
			Nullable: rand.IntN(2) == 0,
		})
	}
	return cols
}

// RandomRow returns a row with random values matching the given schema.
func RandomRow(schema []laredo.ColumnDefinition, id int) laredo.Row {
	row := make(laredo.Row, len(schema))
	for _, col := range schema {
		if col.Name == "id" {
			row["id"] = id
			continue
		}
		row[col.Name] = randomValue(col.Type)
	}
	return row
}

// RandomRows returns n rows with sequential IDs starting from 1.
func RandomRows(schema []laredo.ColumnDefinition, n int) []laredo.Row {
	rows := make([]laredo.Row, n)
	for i := range n {
		rows[i] = RandomRow(schema, i+1)
	}
	return rows
}

// RandomChangeEvent returns a change event with random values.
//
//nolint:gosec // math/rand is fine for test data generation.
func RandomChangeEvent(table laredo.TableIdentifier, schema []laredo.ColumnDefinition, seq int) laredo.ChangeEvent {
	actions := []laredo.ChangeAction{laredo.ActionInsert, laredo.ActionUpdate, laredo.ActionDelete}
	action := actions[rand.IntN(len(actions))]

	event := laredo.ChangeEvent{
		Table:     table,
		Action:    action,
		Position:  seq,
		Timestamp: time.Now(),
	}

	switch action {
	case laredo.ActionInsert:
		event.NewValues = RandomRow(schema, seq)
	case laredo.ActionUpdate:
		event.OldValues = RandomRow(schema, seq)
		event.NewValues = RandomRow(schema, seq)
	case laredo.ActionDelete:
		event.OldValues = RandomRow(schema, seq)
	}

	return event
}

// RandomChangeEvents returns n change events with sequential positions.
func RandomChangeEvents(table laredo.TableIdentifier, schema []laredo.ColumnDefinition, n int) []laredo.ChangeEvent {
	events := make([]laredo.ChangeEvent, n)
	for i := range n {
		events[i] = RandomChangeEvent(table, schema, i+1)
	}
	return events
}

//nolint:gosec // math/rand is fine for test data generation.
func randomValue(colType string) any {
	switch colType {
	case "text":
		words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
		return words[rand.IntN(len(words))] + fmt.Sprintf("-%d", rand.IntN(10000))
	case "integer":
		return rand.IntN(100000)
	case "boolean":
		return rand.IntN(2) == 0
	case "float8":
		return rand.Float64() * 1000
	case "jsonb":
		return fmt.Sprintf(`{"key_%d": %d}`, rand.IntN(10), rand.IntN(100))
	case "timestamp":
		return time.Now().Add(-time.Duration(rand.IntN(86400)) * time.Second).Format(time.RFC3339)
	case "uuid":
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			rand.Uint32(), rand.Uint32()&0xffff, rand.Uint32()&0xffff,
			rand.Uint32()&0xffff, rand.Uint64()&0xffffffffffff)
	default:
		return fmt.Sprintf("val-%d", rand.IntN(10000))
	}
}
