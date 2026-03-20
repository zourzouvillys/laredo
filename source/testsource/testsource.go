// Package testsource provides an in-memory SyncSource for testing.
package testsource

import (
	"context"

	"github.com/zourzouvillys/laredo"
)

// Source is an in-memory SyncSource for integration testing.
type Source struct {
	// TODO: implement
}

// New creates a new test source.
func New() *Source {
	return &Source{}
}

var _ laredo.SyncSource = (*Source)(nil)

func (s *Source) Init(ctx context.Context, config laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	panic("not implemented")
}

func (s *Source) ValidateTables(ctx context.Context, tables []laredo.TableIdentifier) []laredo.ValidationError {
	return nil
}

func (s *Source) Baseline(ctx context.Context, tables []laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	panic("not implemented")
}

func (s *Source) Stream(ctx context.Context, from laredo.Position, handler laredo.ChangeHandler) error {
	panic("not implemented")
}

func (s *Source) Ack(ctx context.Context, position laredo.Position) error { return nil }
func (s *Source) SupportsResume() bool                                    { return false }

func (s *Source) LastAckedPosition(ctx context.Context) (laredo.Position, error) {
	return nil, nil
}

func (s *Source) ComparePositions(a, b laredo.Position) int { return 0 }
func (s *Source) PositionToString(p laredo.Position) string { return "" }

func (s *Source) PositionFromString(str string) (laredo.Position, error) {
	return nil, nil
}

func (s *Source) Pause(ctx context.Context) error  { return nil }
func (s *Source) Resume(ctx context.Context) error { return nil }
func (s *Source) GetLag() laredo.LagInfo           { return laredo.LagInfo{} }
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return laredo.TotalOrder }
func (s *Source) State() laredo.SourceState                   { return laredo.SourceClosed }
func (s *Source) Close(ctx context.Context) error             { return nil }
