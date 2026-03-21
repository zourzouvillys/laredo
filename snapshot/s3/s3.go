// Package s3 implements a SnapshotStore backed by Amazon S3.
package s3

import (
	"context"

	"github.com/zourzouvillys/laredo"
)

// Store implements laredo.SnapshotStore using S3 storage.
type Store struct {
	// TODO: implement
}

// New creates a new S3 snapshot store.
func New( /* opts */ ) *Store {
	return &Store{}
}

var _ laredo.SnapshotStore = (*Store)(nil)

//nolint:revive // implements SnapshotStore.
func (s *Store) Save(ctx context.Context, snapshotID string, metadata laredo.SnapshotMetadata, entries map[laredo.TableIdentifier][]laredo.SnapshotEntry) (laredo.SnapshotDescriptor, error) {
	panic("not implemented")
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Load(ctx context.Context, snapshotID string) (laredo.SnapshotMetadata, map[laredo.TableIdentifier][]laredo.SnapshotEntry, error) {
	panic("not implemented")
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Describe(ctx context.Context, snapshotID string) (laredo.SnapshotDescriptor, error) {
	panic("not implemented")
}

//nolint:revive // implements SnapshotStore.
func (s *Store) List(ctx context.Context, filter *laredo.SnapshotFilter) ([]laredo.SnapshotDescriptor, error) {
	panic("not implemented")
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Delete(ctx context.Context, snapshotID string) error {
	panic("not implemented")
}

//nolint:revive // implements SnapshotStore.
func (s *Store) Prune(ctx context.Context, keep int, table *laredo.TableIdentifier) error {
	panic("not implemented")
}
