package sqs

import "testing"

func TestSink_Name(t *testing.T) {
	if got := New(nil, "https://sqs/q").Name(); got != "sqs:https://sqs/q" {
		t.Fatalf("name = %q", got)
	}
}
