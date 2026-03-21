// Package pg implements a SyncSource backed by PostgreSQL logical replication.
//
// It uses the built-in pgoutput logical decoding plugin and supports two modes:
//   - Ephemeral: temporary replication slot, full baseline every startup.
//   - Stateful: persistent named slot, resume from last ACKed LSN.
package pg

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/zourzouvillys/laredo"
)

// Source implements laredo.SyncSource using PostgreSQL logical replication.
type Source struct {
	cfg     sourceConfig
	conn    connManager
	repl    replicationManager
	schemas map[laredo.TableIdentifier][]laredo.ColumnDefinition
	tables  []laredo.TableIdentifier
	slotLSN LSN // LSN from slot creation

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
		repl:  replicationManager{cfg: cfg},
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

	s.schemas = schemas
	s.tables = config.Tables

	// Set up the replication connection and slot for streaming.
	if err := s.repl.connect(ctx); err != nil {
		s.setState(laredo.SourceError)
		return nil, fmt.Errorf("pg source: %w", err)
	}

	// Determine publication name.
	pubName := s.cfg.slotName + "_pub"
	if s.cfg.publication.Name != "" {
		pubName = s.cfg.publication.Name
	}

	// Create publication if configured.
	if s.cfg.publication.Create {
		if err := s.ensurePublication(ctx, pubName, config.Tables); err != nil {
			s.setState(laredo.SourceError)
			return nil, fmt.Errorf("pg source: %w", err)
		}
	}

	// Create replication slot.
	temporary := s.cfg.slotMode == SlotEphemeral
	slotLSN, err := s.repl.createSlot(ctx, s.cfg.slotName, temporary)
	if err != nil {
		s.setState(laredo.SourceError)
		return nil, fmt.Errorf("pg source: %w", err)
	}
	s.slotLSN = slotLSN

	return schemas, nil
}

// ensurePublication creates or updates the publication for the configured tables.
func (s *Source) ensurePublication(ctx context.Context, pubName string, tables []laredo.TableIdentifier) error {
	conn := s.conn.queryConn
	// Drop existing publication (ignore error if not exists).
	_, _ = conn.Exec(ctx, fmt.Sprintf("DROP PUBLICATION IF EXISTS %s", pgQuoteIdent(pubName)))

	// Build table list.
	tableList := make([]string, 0, len(tables))
	for _, t := range tables {
		tableList = append(tableList, pgQuoteIdent(t.Schema)+"."+pgQuoteIdent(t.Table))
	}

	query := fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s",
		pgQuoteIdent(pubName), strings.Join(tableList, ", "))
	_, err := conn.Exec(ctx, query)
	return err
}

//nolint:revive // implements SyncSource.
func (s *Source) ValidateTables(_ context.Context, _ []laredo.TableIdentifier) []laredo.ValidationError {
	return nil
}

// Baseline performs a consistent point-in-time snapshot using a REPEATABLE READ
// transaction. It captures the current WAL LSN, then reads all rows from each
// table via SELECT *.
//
//nolint:revive // implements SyncSource.
func (s *Source) Baseline(ctx context.Context, tables []laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	lsn, err := s.conn.baseline(ctx, tables, s.schemas, rowCallback)
	if err != nil {
		return nil, fmt.Errorf("pg source: %w", err)
	}
	return lsn, nil
}

// Stream begins streaming changes from the given position using logical replication.
//
//nolint:revive // implements SyncSource.
func (s *Source) Stream(ctx context.Context, from laredo.Position, handler laredo.ChangeHandler) error {
	s.setState(laredo.SourceStreaming)

	startLSN := s.slotLSN
	if from != nil {
		if lsn, ok := from.(LSN); ok {
			startLSN = lsn
		}
	}

	pubName := s.cfg.slotName + "_pub"
	if s.cfg.publication.Name != "" {
		pubName = s.cfg.publication.Name
	}

	err := s.repl.startStreaming(ctx, s.cfg.slotName, pubName, startLSN, s.tables, handler)
	if err != nil && ctx.Err() == nil {
		s.setState(laredo.SourceError)
	}
	return err
}

// Ack confirms that changes up to this position have been durably processed.
//
//nolint:revive // implements SyncSource.
func (s *Source) Ack(ctx context.Context, position laredo.Position) error {
	if lsn, ok := position.(LSN); ok && s.repl.conn != nil {
		return s.repl.sendStandbyStatus(ctx, lsn)
	}
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
	_ = s.repl.close(ctx)
	return s.conn.close(ctx)
}

func (s *Source) setState(state laredo.SourceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}
