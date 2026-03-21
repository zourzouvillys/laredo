package deadletter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/zourzouvillys/laredo"
)

// LocalStore persists dead letters as JSONL files on local disk.
// Each pipeline gets its own file: {basePath}/{pipelineID}.jsonl
type LocalStore struct {
	mu       sync.Mutex
	basePath string
}

// NewLocalStore creates a local disk dead letter store at the given path.
func NewLocalStore(basePath string) *LocalStore {
	return &LocalStore{basePath: basePath}
}

var (
	_ Store                  = (*LocalStore)(nil)
	_ laredo.DeadLetterStore = (*LocalStore)(nil)
)

//nolint:revive // implements Store.
func (s *LocalStore) Write(pipelineID string, change laredo.ChangeEvent, errInfo laredo.ErrorInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.basePath, 0o750); err != nil {
		return fmt.Errorf("deadletter local: mkdir: %w", err)
	}

	entry := Entry{PipelineID: pipelineID, Change: change, Error: errInfo}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("deadletter local: marshal: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(s.filePath(pipelineID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path from config
	if err != nil {
		return fmt.Errorf("deadletter local: open: %w", err)
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("deadletter local: write: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("deadletter local: close: %w", closeErr)
	}
	return nil
}

//nolint:revive // implements Store.
func (s *LocalStore) Read(pipelineID string, limit int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath(pipelineID)) //nolint:gosec // path from config
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("deadletter local: read: %w", err)
	}

	var entries []Entry
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip corrupt lines
		}
		entries = append(entries, entry)
		if limit > 0 && len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

//nolint:revive // implements Store.
func (s *LocalStore) Replay(pipelineID string, target laredo.SyncTarget) error {
	entries, err := s.Read(pipelineID, 0)
	if err != nil {
		return err
	}

	ctx := context.Background()
	for _, entry := range entries {
		var replayErr error
		switch entry.Change.Action {
		case laredo.ActionInsert:
			replayErr = target.OnInsert(ctx, entry.Change.Table, entry.Change.NewValues)
		case laredo.ActionUpdate:
			replayErr = target.OnUpdate(ctx, entry.Change.Table, entry.Change.NewValues, entry.Change.OldValues)
		case laredo.ActionDelete:
			replayErr = target.OnDelete(ctx, entry.Change.Table, entry.Change.OldValues)
		case laredo.ActionTruncate:
			replayErr = target.OnTruncate(ctx, entry.Change.Table)
		}
		if replayErr != nil {
			return fmt.Errorf("deadletter local: replay %s: %w", pipelineID, replayErr)
		}
	}
	return nil
}

//nolint:revive // implements Store.
func (s *LocalStore) Purge(pipelineID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.filePath(pipelineID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deadletter local: purge: %w", err)
	}
	return nil
}

func (s *LocalStore) filePath(pipelineID string) string {
	return filepath.Join(s.basePath, pipelineID+".jsonl")
}

func splitLines(data []byte) [][]byte {
	return slices.DeleteFunc(
		slicesBytesSplit(data, '\n'),
		func(b []byte) bool { return len(b) == 0 },
	)
}

func slicesBytesSplit(data []byte, sep byte) [][]byte {
	var result [][]byte
	for len(data) > 0 {
		idx := -1
		for i, b := range data {
			if b == sep {
				idx = i
				break
			}
		}
		if idx < 0 {
			result = append(result, data)
			break
		}
		result = append(result, data[:idx])
		data = data[idx+1:]
	}
	return result
}
