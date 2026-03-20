// Package local implements a SnapshotStore backed by local disk.
package local

import (
	"context"

	"github.com/zourzouvillys/laredo"
)

// Store implements laredo.SnapshotStore using local disk storage.
type Store struct {
	// TODO: implement
}

// New creates a new local disk snapshot store.
func New( /* opts */ ) *Store {
	return &Store{}
}

var _ laredo.SnapshotStore = (*Store)(nil)

func (s *Store) Save(ctx context.Context, snapshotID string, metadata laredo.SnapshotMetadata, entries map[laredo.TableIdentifier][]laredo.SnapshotEntry) (laredo.SnapshotDescriptor, error) {
	panic("not implemented")
}

func (s *Store) Load(ctx context.Context, snapshotID string) (laredo.SnapshotMetadata, map[laredo.TableIdentifier][]laredo.SnapshotEntry, error) {
	panic("not implemented")
}

func (s *Store) Describe(ctx context.Context, snapshotID string) (laredo.SnapshotDescriptor, error) {
	panic("not implemented")
}

func (s *Store) List(ctx context.Context, filter *laredo.SnapshotFilter) ([]laredo.SnapshotDescriptor, error) {
	panic("not implemented")
}

func (s *Store) Delete(ctx context.Context, snapshotID string) error {
	panic("not implemented")
}

func (s *Store) Prune(ctx context.Context, keep int, table *laredo.TableIdentifier) error {
	panic("not implemented")
}
