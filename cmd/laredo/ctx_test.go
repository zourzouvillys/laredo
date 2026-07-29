package main

import (
	"testing"
	"time"
)

// TestCtx_NotCanceled guards against a regression where ctx() returned an
// already-canceled context (a deferred cancel firing before the request ran),
// which broke every server-talking CLI command with "context canceled".
func TestCtx_NotCanceled(t *testing.T) {
	saved := timeout
	timeout = 5 * time.Second
	defer func() { timeout = saved }()

	c := ctx()
	if err := c.Err(); err != nil {
		t.Fatalf("ctx() returned a context already in error state: %v", err)
	}
	if _, ok := c.Deadline(); !ok {
		t.Error("ctx() should carry a deadline from --timeout")
	}
}
