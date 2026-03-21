// Package pg implements a SyncSource backed by PostgreSQL logical replication.
//
// It uses the built-in pgoutput logical decoding plugin and supports two modes:
//   - Ephemeral: temporary replication slot, full baseline every startup.
//   - Stateful: persistent named slot, resume from last ACKed LSN.
package pg

import (
	"context"
	"fmt"
	"sync"

	"github.com/zourzouvillys/laredo"
)

// Source implements laredo.SyncSource using PostgreSQL logical replication.
type Source struct {
	cfg  sourceConfig
	conn connManager

	mu    sync.Mutex
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
		conn:  connManager{cfg: cfg},
		state: laredo.SourceClosed,
	}
}

var _ laredo.SyncSource = (*Source)(nil)

// Init connects to PostgreSQL and discovers column schemas for the configured
// tables. It establishes the query connection used for baseline SELECTs and
// schema discovery. The replication connection is created lazily when streaming.
//
//nolint:revive // implements SyncSource.
func (s *Source) Init(ctx context.Context, config laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	s.setState(laredo.SourceConnecting)

	if s.cfg.connString == "" {
		s.setState(laredo.SourceError)
		return nil, fmt.Errorf("pg source: connection string is required")
	}

	if err := s.conn.connect(ctx); err != nil {
		s.setState(laredo.SourceError)
		return nil, fmt.Errorf("pg source: %w", err)
	}

	s.setState(laredo.SourceConnected)

	schemas, err := s.conn.discoverSchemas(ctx, config.Tables)
	if err != nil {
		s.setState(laredo.SourceError)
		return nil, fmt.Errorf("pg source: %w", err)
	}

	return schemas, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) ValidateTables(_ context.Context, _ []laredo.TableIdentifier) []laredo.ValidationError {
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) Baseline(_ context.Context, _ []laredo.TableIdentifier, _ func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	return nil, fmt.Errorf("pg source: baseline not yet implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Stream(_ context.Context, _ laredo.Position, _ laredo.ChangeHandler) error {
	return fmt.Errorf("pg source: streaming not yet implemented")
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
	return fmt.Errorf("pg source: pause not yet implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) Resume(_ context.Context) error {
	return fmt.Errorf("pg source: resume not yet implemented")
}

//nolint:revive // implements SyncSource.
func (s *Source) GetLag() laredo.LagInfo { return laredo.LagInfo{} }

//nolint:revive // implements SyncSource.
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return laredo.TotalOrder }

//nolint:revive // implements SyncSource.
func (s *Source) State() laredo.SourceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

//nolint:revive // implements SyncSource.
func (s *Source) Close(ctx context.Context) error {
	s.setState(laredo.SourceClosed)
	return s.conn.close(ctx)
}

func (s *Source) setState(state laredo.SourceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}
