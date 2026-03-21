package engine

import (
	"testing"
	"time"
)

func TestReadinessTracker_AllReady(t *testing.T) {
	rt := NewReadinessTracker([]string{"p1", "p2", "p3"})

	if rt.IsReady() {
		t.Error("expected not ready initially")
	}

	rt.SetReady("p1")
	if rt.IsReady() {
		t.Error("expected not ready after 1 of 3")
	}

	rt.SetReady("p2")
	if rt.IsReady() {
		t.Error("expected not ready after 2 of 3")
	}

	rt.SetReady("p3")
	if !rt.IsReady() {
		t.Error("expected ready after all 3")
	}
}

func TestReadinessTracker_AwaitReadyTimeout(t *testing.T) {
	rt := NewReadinessTracker([]string{"p1"})

	if rt.AwaitReady(10 * time.Millisecond) {
		t.Error("expected timeout")
	}
}

func TestReadinessTracker_AwaitReadySuccess(t *testing.T) {
	rt := NewReadinessTracker([]string{"p1"})

	go func() {
		time.Sleep(5 * time.Millisecond)
		rt.SetReady("p1")
	}()

	if !rt.AwaitReady(1 * time.Second) {
		t.Error("expected ready")
	}
}

func TestReadinessTracker_AwaitReadyZeroTimeout(t *testing.T) {
	rt := NewReadinessTracker([]string{"p1"})

	if rt.AwaitReady(0) {
		t.Error("expected not ready with zero timeout")
	}

	rt.SetReady("p1")

	if !rt.AwaitReady(0) {
		t.Error("expected ready with zero timeout after SetReady")
	}
}

func TestReadinessTracker_Empty(t *testing.T) {
	rt := NewReadinessTracker(nil)

	if !rt.IsReady() {
		t.Error("expected ready with no pipelines")
	}
}

func TestReadinessTracker_DuplicateSetReady(t *testing.T) {
	rt := NewReadinessTracker([]string{"p1"})
	rt.SetReady("p1")
	rt.SetReady("p1") // should not panic
	if !rt.IsReady() {
		t.Error("expected ready")
	}
}
