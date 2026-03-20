// Package transform provides built-in pipeline transforms.
package transform

import (
	"time"

	"github.com/zourzouvillys/laredo"
)

// DropFields removes specified fields from the row.
type DropFields struct {
	Fields []string
}

func (t *DropFields) Transform(table laredo.TableIdentifier, row laredo.Row) laredo.Row {
	return row.Without(t.Fields...)
}

// RenameFields renames fields in the row.
type RenameFields struct {
	Mapping map[string]string // old name -> new name
}

func (t *RenameFields) Transform(table laredo.TableIdentifier, row laredo.Row) laredo.Row {
	out := make(laredo.Row, len(row))
	for k, v := range row {
		if newName, ok := t.Mapping[k]; ok {
			out[newName] = v
		} else {
			out[k] = v
		}
	}
	return out
}

// AddTimestamp adds a field with the current timestamp.
type AddTimestamp struct {
	Field string
}

func (t *AddTimestamp) Transform(table laredo.TableIdentifier, row laredo.Row) laredo.Row {
	out := make(laredo.Row, len(row)+1)
	for k, v := range row {
		out[k] = v
	}
	out[t.Field] = time.Now().UTC()
	return out
}
