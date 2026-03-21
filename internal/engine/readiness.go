package engine

import (
	"sync"
	"time"
)

// ReadinessTracker monitors pipeline readiness. A pipeline is ready when its
// baseline is complete and it has transitioned to streaming. Global readiness
// is achieved when all registered pipelines are ready.
type ReadinessTracker struct {
	mu        sync.Mutex
	ready     map[string]bool
	allReady  chan struct{} // closed when all pipelines are ready
	closed    bool
	callbacks []func()
}

// NewReadinessTracker creates a tracker for the given pipeline IDs.
func NewReadinessTracker(pipelineIDs []string) *ReadinessTracker {
	ready := make(map[string]bool, len(pipelineIDs))
	for _, id := range pipelineIDs {
		ready[id] = false
	}
	rt := &ReadinessTracker{
		ready:    ready,
		allReady: make(chan struct{}),
	}
	if len(pipelineIDs) == 0 {
		rt.closed = true
		close(rt.allReady)
	}
	return rt
}

// SetReady marks a pipeline as ready. When all pipelines are ready, the global
// readiness channel is closed and registered callbacks are fired.
func (r *ReadinessTracker) SetReady(pipelineID string) {
	r.mu.Lock()

	r.ready[pipelineID] = true

	if r.closed {
		r.mu.Unlock()
		return
	}
	for _, v := range r.ready {
		if !v {
			r.mu.Unlock()
			return
		}
	}
	r.closed = true
	close(r.allReady)
	cbs := append([]func(){}, r.callbacks...)
	r.callbacks = nil
	r.mu.Unlock()

	for _, cb := range cbs {
		cb()
	}
}

// OnReady registers a callback to be invoked when all pipelines are ready.
// If already ready, the callback is invoked immediately (synchronously).
// Callbacks are fired outside the lock and must not block.
func (r *ReadinessTracker) OnReady(cb func()) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cb()
		return
	}
	r.callbacks = append(r.callbacks, cb)
	r.mu.Unlock()
}

// IsReady reports whether all pipelines are ready.
func (r *ReadinessTracker) IsReady() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// AwaitReady blocks until all pipelines are ready or the timeout expires.
// Returns true if all pipelines became ready.
func (r *ReadinessTracker) AwaitReady(timeout time.Duration) bool {
	if timeout <= 0 {
		select {
		case <-r.allReady:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-r.allReady:
		return true
	case <-timer.C:
		return false
	}
}
