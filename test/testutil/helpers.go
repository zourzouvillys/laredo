package testutil

import (
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
)

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

// SampleChangeEvent returns a ChangeEvent for use in tests.
func SampleChangeEvent(action laredo.ChangeAction, id int, name string) laredo.ChangeEvent {
	return laredo.ChangeEvent{
		Table:     SampleTable(),
		Action:    action,
		Position:  id, // use id as a simple monotonic position
		Timestamp: time.Now(),
		NewValues: SampleRow(id, name),
	}
}

// AssertEventually polls condition every 10ms until it returns true or timeout
// expires. The deadline is a ceiling, not a delay — it returns the instant the
// condition holds — so under the race detector it is scaled up by
// raceTimeoutScale to absorb the detector's scheduling overhead, with no cost on
// the happy path. See raceflag.go.
func AssertEventually(t testing.TB, timeout time.Duration, condition func() bool, msgAndArgs ...interface{}) {
	t.Helper()
	timeout *= raceTimeoutScale
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(msgAndArgs) > 0 {
		t.Fatalf("condition not met within %v: %v", timeout, msgAndArgs[0])
	} else {
		t.Fatalf("condition not met within %v", timeout)
	}
}
