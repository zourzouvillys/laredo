package sns

import "testing"

func TestSink_Name(t *testing.T) {
	if got := New(nil, "arn:aws:sns:us-east-1:1:t").Name(); got != "sns:arn:aws:sns:us-east-1:1:t" {
		t.Fatalf("name = %q", got)
	}
}
