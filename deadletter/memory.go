package deadletter

import (
	"context"
	"fmt"
	"sync"

	"github.com/zourzouvillys/laredo"
)

// MemoryStore is an in-memory dead letter store for testing and development.
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string][]Entry // keyed by pipeline ID
}

// NewMemoryStore creates a new in-memory dead letter store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		entries: make(map[string][]Entry),
	}
}

var (
	_ Store                  = (*MemoryStore)(nil)
	_ laredo.DeadLetterStore = (*MemoryStore)(nil)
)

//nolint:revive // implements Store.
func (s *MemoryStore) Write(pipelineID string, change laredo.ChangeEvent, err laredo.ErrorInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[pipelineID] = append(s.entries[pipelineID], Entry{
		PipelineID: pipelineID,
		Change:     change,
		Error:      err,
	})
	return nil
}

//nolint:revive // implements Store.
func (s *MemoryStore) Read(pipelineID string, limit int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.entries[pipelineID]
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	// Return a copy.
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out, nil
}

//nolint:revive // implements Store.
func (s *MemoryStore) Replay(pipelineID string, target laredo.SyncTarget) error {
	s.mu.Lock()
	entries := s.entries[pipelineID]
	s.mu.Unlock()

	ctx := context.Background()
	for _, entry := range entries {
		var err error
		switch entry.Change.Action {
		case laredo.ActionInsert:
			err = target.OnInsert(ctx, entry.Change.Table, entry.Change.NewValues)
		case laredo.ActionUpdate:
			err = target.OnUpdate(ctx, entry.Change.Table, entry.Change.NewValues, entry.Change.OldValues)
		case laredo.ActionDelete:
			err = target.OnDelete(ctx, entry.Change.Table, entry.Change.OldValues)
		case laredo.ActionTruncate:
			err = target.OnTruncate(ctx, entry.Change.Table)
		}
		if err != nil {
			return fmt.Errorf("replay %s: %w", pipelineID, err)
		}
	}
	return nil
}

//nolint:revive // implements Store.
func (s *MemoryStore) Purge(pipelineID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, pipelineID)
	return nil
}

// Count returns the number of dead letter entries for a pipeline.
func (s *MemoryStore) Count(pipelineID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries[pipelineID])
}

// Total returns the total number of dead letter entries across all pipelines.
func (s *MemoryStore) Total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, entries := range s.entries {
		total += len(entries)
	}
	return total
}
