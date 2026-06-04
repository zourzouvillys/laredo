package kinesis

import "testing"

func TestSink_Name(t *testing.T) {
	if got := New(nil, "stream-x").Name(); got != "kinesis:stream-x" {
		t.Fatalf("name = %q", got)
	}
}
