package engine

import "sync"

// AckTracker coordinates ACK positions across pipelines sharing a source.
// It tracks the last confirmed (durable) position for each pipeline and
// computes the minimum across all active pipelines for a source. The engine
// ACKs the source at this minimum position.
type AckTracker struct {
	mu sync.Mutex

	// pipelineSource maps pipeline ID → source ID.
	pipelineSource map[string]string

	// positions maps pipeline ID → last confirmed position.
	positions map[string]any

	// lastAcked maps source ID → last ACKed position.
	lastAcked map[string]any

	// compare is the source's position comparator.
	// Returns negative if a < b, zero if equal, positive if a > b.
	comparators map[string]func(a, b any) int

	// skipped tracks pipelines that should be excluded from minimum computation
	// (e.g., ERROR state pipelines under Isolate policy).
	skipped map[string]bool
}

// NewAckTracker creates an ACK tracker.
func NewAckTracker() *AckTracker {
	return &AckTracker{
		pipelineSource: make(map[string]string),
		positions:      make(map[string]any),
		lastAcked:      make(map[string]any),
		comparators:    make(map[string]func(a, b any) int),
		skipped:        make(map[string]bool),
	}
}

// RegisterPipeline registers a pipeline with its source. The comparator is
// used to compare positions for the source.
func (a *AckTracker) RegisterPipeline(pipelineID, sourceID string, compare func(x, y any) int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pipelineSource[pipelineID] = sourceID
	a.comparators[sourceID] = compare
}

// Confirm records that a pipeline has durably processed up to the given position.
func (a *AckTracker) Confirm(pipelineID string, position any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.positions[pipelineID] = position
}

// Skip marks a pipeline as skipped for minimum computation (e.g., ERROR state).
func (a *AckTracker) Skip(pipelineID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.skipped[pipelineID] = true
}

// AckPosition returns the minimum confirmed position across all active
// (non-skipped) pipelines for the given source. Returns nil if no position
// can be computed (no active pipelines or no confirmed positions).
// Also returns whether the position has advanced beyond the last ACKed position.
func (a *AckTracker) AckPosition(sourceID string) (position any, advanced bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	compare := a.comparators[sourceID]
	if compare == nil {
		return nil, false
	}

	var minPos any
	for pid, sid := range a.pipelineSource {
		if sid != sourceID || a.skipped[pid] {
			continue
		}
		pos, ok := a.positions[pid]
		if !ok {
			// Pipeline hasn't confirmed anything yet — can't advance.
			return nil, false
		}
		if minPos == nil || compare(pos, minPos) < 0 {
			minPos = pos
		}
	}

	if minPos == nil {
		return nil, false
	}

	lastAcked := a.lastAcked[sourceID]
	if lastAcked != nil && compare(minPos, lastAcked) <= 0 {
		return minPos, false
	}

	a.lastAcked[sourceID] = minPos
	return minPos, true
}

// LastAcked returns the last ACKed position for a source, or nil if none.
func (a *AckTracker) LastAcked(sourceID string) any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastAcked[sourceID]
}
