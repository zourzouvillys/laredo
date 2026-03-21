// Package local implements a SnapshotStore backed by local disk.
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshot/jsonl"
)

// Store implements laredo.SnapshotStore using local disk storage.
type Store struct {
	basePath string
}

// New creates a new local disk snapshot store rooted at basePath.
func New(basePath string) *Store {
	return &Store{basePath: basePath}
}

var _ laredo.SnapshotStore = (*Store)(nil)

//nolint:revive // implements SnapshotStore.
func (s *Store) Save(_ context.Context, snapshotID string, metadata laredo.SnapshotMetadata, entries map[laredo.TableIdentifier][]laredo.SnapshotEntry) (laredo.SnapshotDescriptor, error) {
	tmpDir := filepath.Join(s.basePath, ".tmp-"+snapshotID)
	finalDir := filepath.Join(s.basePath, snapshotID)

	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return laredo.SnapshotDescriptor{}, fmt.Errorf("local: create temp dir: %w", err)
	}

	// Clean up temp dir on error.
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	// Write metadata.json.
	metaBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return laredo.SnapshotDescriptor{}, fmt.Errorf("local: marshal metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "metadata.json"), metaBytes, 0o600); err != nil {
		return laredo.SnapshotDescriptor{}, fmt.Errorf("local: write metadata: %w", err)
	}

	ser := jsonl.New()
	var tableInfos []laredo.TableSnapshotInfo

	for table, tableEntries := range entries {
		rows := make([]laredo.Row, len(tableEntries))
		for i, e := range tableEntries {
			rows[i] = e.Row
		}

		// Find matching TableSnapshotInfo from metadata, or build a minimal one.
		info := findTableInfo(metadata, table, int64(len(rows)))

		filename := table.Schema + "." + table.Table + ".jsonl"
		f, err := os.Create(filepath.Join(tmpDir, filename)) //nolint:gosec // path constructed from snapshot directory
		if err != nil {
			return laredo.SnapshotDescriptor{}, fmt.Errorf("local: create table file %s: %w", filename, err)
		}

		writeErr := ser.Write(info, rows, f)
		closeErr := f.Close()
		if writeErr != nil {
			return laredo.SnapshotDescriptor{}, fmt.Errorf("local: write table %s: %w", table, writeErr)
		}
		if closeErr != nil {
			return laredo.SnapshotDescriptor{}, fmt.Errorf("local: close table file %s: %w", filename, closeErr)
		}

		tableInfos = append(tableInfos, info)
	}

	// Rename temp dir to final.
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return laredo.SnapshotDescriptor{}, fmt.Errorf("local: rename snapshot dir: %w", err)
	}
	success = true

	totalSize, _ := dirSize(finalDir)

	return laredo.SnapshotDescriptor{
		SnapshotID: snapshotID,
		CreatedAt:  metadata.CreatedAt,
		Format:     "jsonl",
		Tables:     tableInfos,
		SizeBytes:  totalSize,
	}, nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Load(_ context.Context, snapshotID string) (laredo.SnapshotMetadata, map[laredo.TableIdentifier][]laredo.SnapshotEntry, error) {
	snapshotDir := filepath.Join(s.basePath, snapshotID)

	metaBytes, err := os.ReadFile(filepath.Join(snapshotDir, "metadata.json")) //nolint:gosec // path constructed from snapshot directory
	if err != nil {
		return laredo.SnapshotMetadata{}, nil, fmt.Errorf("local: read metadata for %s: %w", snapshotID, err)
	}

	var metadata laredo.SnapshotMetadata
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		return laredo.SnapshotMetadata{}, nil, fmt.Errorf("local: unmarshal metadata for %s: %w", snapshotID, err)
	}

	ser := jsonl.New()
	entries := make(map[laredo.TableIdentifier][]laredo.SnapshotEntry)

	for _, tableInfo := range metadata.Tables {
		filename := tableInfo.Table.Schema + "." + tableInfo.Table.Table + ".jsonl"
		f, err := os.Open(filepath.Join(snapshotDir, filename)) //nolint:gosec // path constructed from snapshot directory
		if err != nil {
			return metadata, nil, fmt.Errorf("local: open table file %s: %w", filename, err)
		}

		_, rows, readErr := ser.Read(f)
		closeErr := f.Close()
		if readErr != nil {
			return metadata, nil, fmt.Errorf("local: read table %s: %w", tableInfo.Table, readErr)
		}
		if closeErr != nil {
			return metadata, nil, fmt.Errorf("local: close table file %s: %w", filename, closeErr)
		}

		snapshotEntries := make([]laredo.SnapshotEntry, len(rows))
		for i, row := range rows {
			snapshotEntries[i] = laredo.SnapshotEntry{Row: row}
		}
		entries[tableInfo.Table] = snapshotEntries
	}

	return metadata, entries, nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Describe(_ context.Context, snapshotID string) (laredo.SnapshotDescriptor, error) {
	snapshotDir := filepath.Join(s.basePath, snapshotID)

	metadata, err := readMetadata(snapshotDir)
	if err != nil {
		return laredo.SnapshotDescriptor{}, fmt.Errorf("local: describe %s: %w", snapshotID, err)
	}

	totalSize, _ := dirSize(snapshotDir)

	return laredo.SnapshotDescriptor{
		SnapshotID: metadata.SnapshotID,
		CreatedAt:  metadata.CreatedAt,
		Format:     metadata.Format,
		Tables:     metadata.Tables,
		SizeBytes:  totalSize,
	}, nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) List(_ context.Context, filter *laredo.SnapshotFilter) ([]laredo.SnapshotDescriptor, error) {
	dirEntries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("local: list snapshots: %w", err)
	}

	var descriptors []laredo.SnapshotDescriptor

	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".tmp-") {
			continue
		}

		snapshotDir := filepath.Join(s.basePath, name)
		metadata, err := readMetadata(snapshotDir)
		if err != nil {
			// Skip snapshots with unreadable metadata.
			continue
		}

		// Apply table filter.
		if filter != nil && filter.Table != nil {
			if !containsTable(metadata.Tables, *filter.Table) {
				continue
			}
		}

		totalSize, _ := dirSize(snapshotDir)

		descriptors = append(descriptors, laredo.SnapshotDescriptor{
			SnapshotID: metadata.SnapshotID,
			CreatedAt:  metadata.CreatedAt,
			Format:     metadata.Format,
			Tables:     metadata.Tables,
			SizeBytes:  totalSize,
		})
	}

	// Sort by CreatedAt descending (newest first).
	slices.SortFunc(descriptors, func(a, b laredo.SnapshotDescriptor) int {
		if a.CreatedAt.After(b.CreatedAt) {
			return -1
		}
		if a.CreatedAt.Before(b.CreatedAt) {
			return 1
		}
		return 0
	})

	return descriptors, nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Delete(_ context.Context, snapshotID string) error {
	snapshotDir := filepath.Join(s.basePath, snapshotID)
	if _, err := os.Stat(snapshotDir); err != nil {
		return fmt.Errorf("local: delete %s: %w", snapshotID, err)
	}
	if err := os.RemoveAll(snapshotDir); err != nil {
		return fmt.Errorf("local: delete %s: %w", snapshotID, err)
	}
	return nil
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Prune(ctx context.Context, keep int, table *laredo.TableIdentifier) error {
	var filter *laredo.SnapshotFilter
	if table != nil {
		filter = &laredo.SnapshotFilter{Table: table}
	}

	snapshots, err := s.List(ctx, filter)
	if err != nil {
		return fmt.Errorf("local: prune: %w", err)
	}

	// List returns newest first; delete everything beyond keep.
	if len(snapshots) <= keep {
		return nil
	}

	for _, snap := range snapshots[keep:] {
		if err := s.Delete(ctx, snap.SnapshotID); err != nil {
			return fmt.Errorf("local: prune: %w", err)
		}
	}

	return nil
}

// findTableInfo looks up the TableSnapshotInfo for a table in the metadata,
// or creates a minimal one if not found.
func findTableInfo(metadata laredo.SnapshotMetadata, table laredo.TableIdentifier, rowCount int64) laredo.TableSnapshotInfo {
	for _, info := range metadata.Tables {
		if info.Table == table {
			return info
		}
	}
	return laredo.TableSnapshotInfo{
		Table:    table,
		RowCount: rowCount,
	}
}

// readMetadata reads and parses the metadata.json file in a snapshot directory.
func readMetadata(snapshotDir string) (laredo.SnapshotMetadata, error) {
	data, err := os.ReadFile(filepath.Join(snapshotDir, "metadata.json")) //nolint:gosec // path constructed from snapshot directory
	if err != nil {
		return laredo.SnapshotMetadata{}, err
	}
	var metadata laredo.SnapshotMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return laredo.SnapshotMetadata{}, err
	}
	return metadata, nil
}

// dirSize calculates the total size of all files in a directory (non-recursive).
func dirSize(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total, nil
}

// containsTable checks whether a table appears in the list of table snapshot infos.
func containsTable(tables []laredo.TableSnapshotInfo, table laredo.TableIdentifier) bool {
	for _, t := range tables {
		if t.Table == table {
			return true
		}
	}
	return false
}
