// Package filter provides built-in pipeline filters.
package filter

import (
	"regexp"

	"github.com/zourzouvillys/laredo"
)

// FieldEquals matches rows where a field equals a value.
type FieldEquals struct {
	Field string
	Value any
}

//nolint:revive // implements PipelineFilter.
func (f *FieldEquals) Include(table laredo.TableIdentifier, row laredo.Row) bool {
	return row[f.Field] == f.Value
}

// FieldPrefix matches rows where a string field starts with a prefix.
type FieldPrefix struct {
	Field  string
	Prefix string
}

//nolint:revive // implements PipelineFilter.
func (f *FieldPrefix) Include(table laredo.TableIdentifier, row laredo.Row) bool {
	if s, ok := row[f.Field].(string); ok {
		return len(s) >= len(f.Prefix) && s[:len(f.Prefix)] == f.Prefix
	}
	return false
}

// FieldRegex matches rows where a field matches a regular expression.
type FieldRegex struct {
	Field   string
	Pattern *regexp.Regexp
}

//nolint:revive // implements PipelineFilter.
func (f *FieldRegex) Include(table laredo.TableIdentifier, row laredo.Row) bool {
	if s, ok := row[f.Field].(string); ok {
		return f.Pattern.MatchString(s)
	}
	return false
}
