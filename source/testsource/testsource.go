// Package testsource provides an in-memory SyncSource for integration testing.
//
// It is fully programmable: callers configure table schemas and baseline rows
// before the engine starts, then emit change events from test goroutines while
// the engine streams.
package testsource

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
)

// Source is an in-memory SyncSource for integration testing.
//
// Create with [New], configure schemas with [Source.SetSchema] and baseline
// rows with [Source.AddRow], then pass to the engine. After the engine calls
// [Source.Stream], inject changes with [Source.EmitInsert], [Source.EmitUpdate],
// [Source.EmitDelete], and [Source.EmitTruncate].
type Source struct {
	mu sync.Mutex

	// Schema configuration (set before Init).
	schemas map[laredo.TableIdentifier][]laredo.ColumnDefinition
	rows    map[laredo.TableIdentifier][]laredo.Row

	// Runtime state.
	state    laredo.SourceState
	seq      uint64 // monotonic position counter
	lastAck  *uint64
	changeCh chan changeRequest
	closed   bool

	// Error injection.
	initErr     error
	baselineErr error
}

// changeRequest wraps a change event sent from test goroutines to the Stream loop.
type changeRequest struct {
	event laredo.ChangeEvent
}

// New creates a new test source.
func New() *Source {
	return &Source{
		schemas:  make(map[laredo.TableIdentifier][]laredo.ColumnDefinition),
		rows:     make(map[laredo.TableIdentifier][]laredo.Row),
		state:    laredo.SourceClosed,
		changeCh: make(chan changeRequest),
	}
}

// SetSchema configures the column definitions for a table. Call before Init.
func (s *Source) SetSchema(table laredo.TableIdentifier, columns []laredo.ColumnDefinition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schemas[table] = columns
}

// AddRow adds a baseline row for a table. Call before Baseline.
func (s *Source) AddRow(table laredo.TableIdentifier, row laredo.Row) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[table] = append(s.rows[table], row)
}

// SetInitError injects an error that Init will return.
func (s *Source) SetInitError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initErr = err
}

// SetBaselineError injects an error that Baseline will return.
func (s *Source) SetBaselineError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baselineErr = err
}

// nextSeq increments and returns the next monotonic sequence number.
// Caller must hold s.mu.
func (s *Source) nextSeq() uint64 {
	s.seq++
	return s.seq
}

// EmitInsert sends an INSERT change event to the stream.
// Blocks until Stream consumes it (or the source is closed).
func (s *Source) EmitInsert(table laredo.TableIdentifier, row laredo.Row) {
	s.mu.Lock()
	pos := s.nextSeq()
	ch := s.changeCh
	s.mu.Unlock()

	ch <- changeRequest{
		event: laredo.ChangeEvent{
			Table:     table,
			Action:    laredo.ActionInsert,
			Position:  pos,
			Timestamp: time.Now(),
			NewValues: row,
		},
	}
}

// EmitUpdate sends an UPDATE change event to the stream.
// Blocks until Stream consumes it (or the source is closed).
func (s *Source) EmitUpdate(table laredo.TableIdentifier, newValues, oldValues laredo.Row) {
	s.mu.Lock()
	pos := s.nextSeq()
	ch := s.changeCh
	s.mu.Unlock()

	ch <- changeRequest{
		event: laredo.ChangeEvent{
			Table:     table,
			Action:    laredo.ActionUpdate,
			Position:  pos,
			Timestamp: time.Now(),
			NewValues: newValues,
			OldValues: oldValues,
		},
	}
}

// EmitDelete sends a DELETE change event to the stream.
// Blocks until Stream consumes it (or the source is closed).
func (s *Source) EmitDelete(table laredo.TableIdentifier, identity laredo.Row) {
	s.mu.Lock()
	pos := s.nextSeq()
	ch := s.changeCh
	s.mu.Unlock()

	ch <- changeRequest{
		event: laredo.ChangeEvent{
			Table:     table,
			Action:    laredo.ActionDelete,
			Position:  pos,
			Timestamp: time.Now(),
			OldValues: identity,
		},
	}
}

// EmitTruncate sends a TRUNCATE change event to the stream.
// Blocks until Stream consumes it (or the source is closed).
func (s *Source) EmitTruncate(table laredo.TableIdentifier) {
	s.mu.Lock()
	pos := s.nextSeq()
	ch := s.changeCh
	s.mu.Unlock()

	ch <- changeRequest{
		event: laredo.ChangeEvent{
			Table:     table,
			Action:    laredo.ActionTruncate,
			Position:  pos,
			Timestamp: time.Now(),
		},
	}
}

var _ laredo.SyncSource = (*Source)(nil)

//nolint:revive // implements SyncSource.
func (s *Source) Init(_ context.Context, config laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.initErr != nil {
		return nil, s.initErr
	}

	result := make(map[laredo.TableIdentifier][]laredo.ColumnDefinition, len(config.Tables))
	for _, table := range config.Tables {
		cols, ok := s.schemas[table]
		if !ok {
			return nil, fmt.Errorf("testsource: table %s not configured via SetSchema", table)
		}
		result[table] = cols
	}

	s.state = laredo.SourceConnected
	return result, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) ValidateTables(_ context.Context, tables []laredo.TableIdentifier) []laredo.ValidationError {
	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []laredo.ValidationError
	for _, table := range tables {
		if _, ok := s.schemas[table]; !ok {
			t := table
			errs = append(errs, laredo.ValidationError{
				Table:   &t,
				Code:    "TABLE_NOT_FOUND",
				Message: "table not configured in test source",
			})
		}
	}
	return errs
}

//nolint:revive // implements SyncSource.
func (s *Source) Baseline(_ context.Context, tables []laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.baselineErr != nil {
		return nil, s.baselineErr
	}

	for _, table := range tables {
		for _, row := range s.rows[table] {
			rowCallback(table, row)
		}
	}

	pos := s.nextSeq()
	return pos, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) Stream(ctx context.Context, _ laredo.Position, handler laredo.ChangeHandler) error {
	s.mu.Lock()
	s.state = laredo.SourceStreaming
	ch := s.changeCh
	s.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.state = laredo.SourceClosed
			s.mu.Unlock()
			return ctx.Err()
		case req, ok := <-ch:
			if !ok {
				// Channel closed by Close().
				s.mu.Lock()
				s.state = laredo.SourceClosed
				s.mu.Unlock()
				return nil
			}
			if err := handler.OnChange(req.event); err != nil {
				return fmt.Errorf("testsource: handler error: %w", err)
			}
		}
	}
}

//nolint:revive // implements SyncSource.
func (s *Source) Ack(_ context.Context, position laredo.Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pos, ok := position.(uint64)
	if !ok {
		return fmt.Errorf("testsource: expected uint64 position, got %T", position)
	}
	s.lastAck = &pos
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) SupportsResume() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAck != nil
}

//nolint:revive // implements SyncSource.
func (s *Source) LastAckedPosition(_ context.Context) (laredo.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastAck == nil {
		return nil, nil
	}
	return *s.lastAck, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) ComparePositions(a, b laredo.Position) int {
	posA, okA := a.(uint64)
	posB, okB := b.(uint64)
	if !okA || !okB {
		return 0
	}
	switch {
	case posA < posB:
		return -1
	case posA > posB:
		return 1
	default:
		return 0
	}
}

//nolint:revive // implements SyncSource.
func (s *Source) PositionToString(p laredo.Position) string {
	pos, ok := p.(uint64)
	if !ok {
		return ""
	}
	return strconv.FormatUint(pos, 10)
}

//nolint:revive // implements SyncSource.
func (s *Source) PositionFromString(str string) (laredo.Position, error) {
	pos, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("testsource: invalid position %q: %w", str, err)
	}
	return pos, nil
}

//nolint:revive // implements SyncSource.
func (s *Source) Pause(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = laredo.SourcePaused
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) Resume(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = laredo.SourceStreaming
	return nil
}

//nolint:revive // implements SyncSource.
func (s *Source) GetLag() laredo.LagInfo {
	return laredo.LagInfo{}
}

//nolint:revive // implements SyncSource.
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee {
	return laredo.TotalOrder
}

//nolint:revive // implements SyncSource.
func (s *Source) State() laredo.SourceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

//nolint:revive // implements SyncSource.
func (s *Source) Close(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.closed {
		s.closed = true
		close(s.changeCh)
	}
	s.state = laredo.SourceClosed
	return nil
}
