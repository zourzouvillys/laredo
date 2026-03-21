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
	"time"

	"github.com/zourzouvillys/laredo"
)

// Source implements laredo.SyncSource using PostgreSQL logical replication.
type Source struct {
	cfg         sourceConfig
	conn        connManager
	repl        replicationManager
	schemas     map[laredo.TableIdentifier][]laredo.ColumnDefinition
	tables      []laredo.TableIdentifier
	slotLSN     LSN  // LSN from slot creation
	slotExisted bool // true if we reused an existing slot (stateful resume)

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

	// Create replication slot (or reuse existing in stateful mode).
	temporary := s.cfg.slotMode == SlotEphemeral
	if s.cfg.slotMode == SlotStateful {
		// Check if slot already exists.
		var exists bool
		_ = s.conn.queryConn.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name = $1)",
			s.cfg.slotName,
		).Scan(&exists)
		if exists {
			// Slot exists — reuse it. Get the confirmed flush LSN.
			s.slotExisted = true
			var lsnStr *string
			_ = s.conn.queryConn.QueryRow(ctx,
				"SELECT confirmed_flush_lsn::text FROM pg_replication_slots WHERE slot_name = $1",
				s.cfg.slotName,
			).Scan(&lsnStr)
			if lsnStr != nil {
				if lsn, err := ParseLSN(*lsnStr); err == nil {
					s.slotLSN = lsn
				}
			}
			return schemas, nil
		}
	}

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
// Stream begins streaming with automatic reconnect on transient failures.
// Uses exponential backoff between reconnection attempts.
//
//nolint:revive // implements SyncSource.
func (s *Source) Stream(ctx context.Context, from laredo.Position, handler laredo.ChangeHandler) error {
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

	backoff := s.cfg.reconnect.InitialBackoff
	if backoff == 0 {
		backoff = 1 * time.Second
	}

	for attempt := 0; ; attempt++ {
		s.setState(laredo.SourceStreaming)

		err := s.repl.startStreaming(ctx, s.cfg.slotName, pubName, startLSN, s.tables, handler)

		// Clean exit (context cancelled).
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}

		// Check if we've exhausted reconnect attempts.
		maxAttempts := s.cfg.reconnect.MaxAttempts
		if maxAttempts == 0 {
			maxAttempts = 10
		}
		if attempt >= maxAttempts {
			s.setState(laredo.SourceError)
			return fmt.Errorf("pg source: exhausted %d reconnect attempts: %w", maxAttempts, err)
		}

		// Reconnect: close old connection, create new one, retry.
		s.setState(laredo.SourceReconnecting)
		_ = s.repl.close(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		if err := s.repl.connect(ctx); err != nil {
			// Connection failed — will retry on next iteration.
			continue
		}

		// Grow backoff.
		multiplier := s.cfg.reconnect.Multiplier
		if multiplier == 0 {
			multiplier = 2.0
		}
		backoff = time.Duration(float64(backoff) * multiplier)
		maxBackoff := s.cfg.reconnect.MaxBackoff
		if maxBackoff == 0 {
			maxBackoff = 30 * time.Second
		}
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
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

// LastAckedPosition queries the replication slot for the confirmed flush LSN.
// Returns nil if the slot doesn't exist or has no confirmed position.
//
//nolint:revive // implements SyncSource.
func (s *Source) LastAckedPosition(_ context.Context) (laredo.Position, error) {
	// Only return a position if we reused an existing slot (stateful resume).
	// For newly created slots, return nil so the engine runs baseline first.
	if !s.slotExisted {
		return nil, nil
	}
	if s.slotLSN == 0 {
		return nil, nil
	}
	return s.slotLSN, nil
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

// Pause pauses the source by setting state to PAUSED. The replication
// stream continues running but the engine won't deliver changes.
//
//nolint:revive // implements SyncSource.
func (s *Source) Pause(_ context.Context) error {
	s.setState(laredo.SourcePaused)
	return nil
}

// Resume resumes the source after a pause.
//
//nolint:revive // implements SyncSource.
func (s *Source) Resume(_ context.Context) error {
	s.setState(laredo.SourceStreaming)
	return nil
}

// GetLag queries the replication slot for lag information.
//
//nolint:revive // implements SyncSource.
func (s *Source) GetLag() laredo.LagInfo {
	if s.conn.queryConn == nil {
		return laredo.LagInfo{}
	}

	var lagBytes *int64
	_ = s.conn.queryConn.QueryRow(context.Background(),
		`SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn)::bigint
		 FROM pg_replication_slots WHERE slot_name = $1`,
		s.cfg.slotName,
	).Scan(&lagBytes)

	if lagBytes == nil {
		return laredo.LagInfo{}
	}
	return laredo.LagInfo{LagBytes: *lagBytes}
}

//nolint:revive // implements SyncSource.
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return laredo.TotalOrder }

//nolint:revive // implements SyncSource.
func (s *Source) State() laredo.SourceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// ResetSource drops and recreates the replication slot and optionally the
// publication. This is a destructive operation — the next startup will
// perform a full baseline.
func (s *Source) ResetSource(ctx context.Context, dropPublication bool) error {
	// Drop the replication slot.
	if s.repl.conn != nil {
		if err := s.repl.dropSlot(ctx, s.cfg.slotName); err != nil {
			return fmt.Errorf("drop slot %q: %w", s.cfg.slotName, err)
		}
	}

	// Drop publication if requested.
	if dropPublication && s.conn.queryConn != nil {
		pubName := s.cfg.slotName + "_pub"
		if s.cfg.publication.Name != "" {
			pubName = s.cfg.publication.Name
		}
		_, _ = s.conn.queryConn.Exec(ctx, fmt.Sprintf("DROP PUBLICATION IF EXISTS %s", pgQuoteIdent(pubName)))
	}

	s.slotExisted = false
	s.slotLSN = 0
	return nil
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
