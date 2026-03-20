package testutil

import "github.com/zourzouvillys/laredo"

// SampleTable returns a TableIdentifier for use in tests.
func SampleTable() laredo.TableIdentifier {
	return laredo.Table("public", "test_table")
}

// SampleColumns returns a basic column definition list for use in tests.
func SampleColumns() []laredo.ColumnDefinition {
	return []laredo.ColumnDefinition{
		{Name: "id", Type: "integer", Nullable: false, PrimaryKey: true},
		{Name: "name", Type: "text", Nullable: true, PrimaryKey: false},
		{Name: "value", Type: "jsonb", Nullable: true, PrimaryKey: false},
	}
}

// SampleRow returns a Row for use in tests.
func SampleRow(id int, name string) laredo.Row {
	return laredo.Row{
		"id":   id,
		"name": name,
	}
}
