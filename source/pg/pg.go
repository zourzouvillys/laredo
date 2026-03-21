// Package pg implements a SyncSource backed by PostgreSQL logical replication.
package pg

import (
	"context"

	"github.com/zourzouvillys/laredo"
)

// Source implements laredo.SyncSource using PostgreSQL logical replication.
type Source struct {
	// TODO: implement
}

// New creates a new PostgreSQL source.
func New( /* opts */ ) *Source {
	return &Source{}
}

var _ laredo.SyncSource = (*Source)(nil)

//nolint:revive // implements SyncSource.
func (s *Source) Init(ctx context.Context, config laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	panic("not implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) ValidateTables(ctx context.Context, tables []laredo.TableIdentifier) []laredo.ValidationError {
	panic("not implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Baseline(ctx context.Context, tables []laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	panic("not implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Stream(ctx context.Context, from laredo.Position, handler laredo.ChangeHandler) error {
	panic("not implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Ack(ctx context.Context, position laredo.Position) error {
	panic("not implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) SupportsResume() bool { return false }

//nolint:revive // implements SyncSource.
func (s *Source) LastAckedPosition(ctx context.Context) (laredo.Position, error) {
	return nil, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) ComparePositions(a, b laredo.Position) int { panic("not implemented") }

//nolint:revive // implements SyncSource.
func (s *Source) PositionToString(p laredo.Position) string { panic("not implemented") }

//nolint:revive // implements SyncSource.
func (s *Source) PositionFromString(str string) (laredo.Position, error) {
	panic("not implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Pause(ctx context.Context) error { panic("not implemented") }

//nolint:revive // implements SyncSource.
func (s *Source) Resume(ctx context.Context) error { panic("not implemented") }

//nolint:revive // implements SyncSource.
func (s *Source) GetLag() laredo.LagInfo { return laredo.LagInfo{} }

//nolint:revive // implements SyncSource.
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return laredo.TotalOrder }

//nolint:revive // implements SyncSource.
func (s *Source) State() laredo.SourceState { return laredo.SourceClosed }

//nolint:revive // implements SyncSource.
func (s *Source) Close(ctx context.Context) error { return nil }
