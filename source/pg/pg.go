// Package pg implements a SyncSource backed by PostgreSQL logical replication.
//
// It uses the built-in pgoutput logical decoding plugin and supports two modes:
//   - Ephemeral: temporary replication slot, full baseline every startup.
//   - Stateful: persistent named slot, resume from last ACKed LSN.
package pg

import (
	"context"
	"fmt"

	"github.com/zourzouvillys/laredo"
)

// Source implements laredo.SyncSource using PostgreSQL logical replication.
type Source struct {
	cfg   sourceConfig
	state laredo.SourceState
}

// New creates a new PostgreSQL source with the given options.
func New(opts ...Option) *Source {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Source{
		cfg:   cfg,
		state: laredo.SourceClosed,
	}
}

var _ laredo.SyncSource = (*Source)(nil)

//nolint:revive // implements SyncSource.
func (s *Source) Init(_ context.Context, _ laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	return nil, fmt.Errorf("pg source: not yet implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) ValidateTables(_ context.Context, _ []laredo.TableIdentifier) []laredo.ValidationError {
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) Baseline(_ context.Context, _ []laredo.TableIdentifier, _ func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	return nil, fmt.Errorf("pg source: not yet implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Stream(_ context.Context, _ laredo.Position, _ laredo.ChangeHandler) error {
	return fmt.Errorf("pg source: not yet implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Ack(_ context.Context, _ laredo.Position) error {
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) SupportsResume() bool {
	return s.cfg.slotMode == SlotStateful
}

//nolint:revive // implements SyncSource.
func (s *Source) LastAckedPosition(_ context.Context) (laredo.Position, error) {
	return nil, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) ComparePositions(a, b laredo.Position) int {
	return CompareLSN(a, b)
}

//nolint:revive // implements SyncSource.
func (s *Source) PositionToString(p laredo.Position) string {
	if lsn, ok := p.(LSN); ok {
		return lsn.String()
	}
	return fmt.Sprintf("%v", p)
}

//nolint:revive // implements SyncSource.
func (s *Source) PositionFromString(str string) (laredo.Position, error) {
	lsn, err := ParseLSN(str)
	if err != nil {
		return nil, err
	}
	return lsn, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) Pause(_ context.Context) error {
	return fmt.Errorf("pg source: not yet implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Resume(_ context.Context) error {
	return fmt.Errorf("pg source: not yet implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) GetLag() laredo.LagInfo { return laredo.LagInfo{} }

//nolint:revive // implements SyncSource.
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return laredo.TotalOrder }

//nolint:revive // implements SyncSource.
func (s *Source) State() laredo.SourceState { return s.state }

//nolint:revive // implements SyncSource.
func (s *Source) Close(_ context.Context) error { return nil }
