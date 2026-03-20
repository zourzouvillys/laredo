package filter

import (
	"regexp"
	"testing"

	"github.com/zourzouvillys/laredo"
)

var table = laredo.Table("public", "test")

func TestFieldEquals(t *testing.T) {
	f := &FieldEquals{Field: "status", Value: "active"}

	tests := []struct {
		name string
		row  laredo.Row
		want bool
	}{
		{"match", laredo.Row{"status": "active"}, true},
		{"no match", laredo.Row{"status": "inactive"}, false},
		{"missing field", laredo.Row{"other": "value"}, false},
		{"nil value", laredo.Row{"status": nil}, false},
		{"empty row", laredo.Row{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Include(table, tt.row); got != tt.want {
				t.Errorf("Include() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFieldPrefix(t *testing.T) {
	f := &FieldPrefix{Field: "key", Prefix: "rulesets/"}

	tests := []struct {
		name string
		row  laredo.Row
		want bool
	}{
		{"match", laredo.Row{"key": "rulesets/default"}, true},
		{"exact prefix", laredo.Row{"key": "rulesets/"}, true},
		{"no match", laredo.Row{"key": "config/settings"}, false},
		{"shorter than prefix", laredo.Row{"key": "rule"}, false},
		{"not a string", laredo.Row{"key": 123}, false},
		{"missing field", laredo.Row{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Include(table, tt.row); got != tt.want {
				t.Errorf("Include() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFieldRegex(t *testing.T) {
	f := &FieldRegex{Field: "name", Pattern: regexp.MustCompile(`^user_\d+$`)}

	tests := []struct {
		name string
		row  laredo.Row
		want bool
	}{
		{"match", laredo.Row{"name": "user_123"}, true},
		{"no match", laredo.Row{"name": "admin"}, false},
		{"partial match", laredo.Row{"name": "user_abc"}, false},
		{"not a string", laredo.Row{"name": 42}, false},
		{"missing field", laredo.Row{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Include(table, tt.row); got != tt.want {
				t.Errorf("Include() = %v, want %v", got, tt.want)
			}
		})
	}
}
