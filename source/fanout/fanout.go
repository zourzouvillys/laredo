// Package fanout provides a laredo SyncSource that consumes an upstream laredo
// fan-out, so a downstream engine can treat a fan-out as a source — cascading
// replication for edge and regional trees. It is a thin adapter over
// client/fanout (which already does snapshot, resume, cold-tier replay, and
// cross-instance failover). See docs/edr/0004-cascading-fanout-source.md.
package fanout

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
	clientfanout "github.com/zourzouvillys/laredo/client/fanout"
)

// Comparator orders two opaque source positions: negative if a<b, zero if a==b,
// positive if a>b.
type Comparator func(a, b string) int

type config struct {
	serverAddress string
	schema        string
	table         string
	clientID      string
	readyTimeout  time.Duration
	cmp           Comparator
	ordering      laredo.OrderingGuarantee
}

// Option configures a fan-out Source.
type Option func(*config)

// ServerAddress sets the upstream fan-out gRPC address ("host:port").
func ServerAddress(addr string) Option { return func(c *config) { c.serverAddress = addr } }

// Table sets the schema and table to consume from the upstream fan-out.
func Table(schema, table string) Option {
	return func(c *config) { c.schema = schema; c.table = table }
}

// ClientID sets the client identifier reported to the upstream for monitoring.
func ClientID(id string) Option { return func(c *config) { c.clientID = id } }

// ReadyTimeout bounds the wait for the upstream's initial snapshot (default 30s).
func ReadyTimeout(d time.Duration) Option { return func(c *config) { c.readyTimeout = d } }

// WithPositionComparator overrides how source positions are ordered. The default
// is PostgreSQL WAL-LSN order (the dominant upstream); override it when the
// upstream fan-out fronts a non-PostgreSQL source.
func WithPositionComparator(cmp Comparator) Option { return func(c *config) { c.cmp = cmp } }

// WithOrderingGuarantee sets the ordering guarantee reported to the engine.
// Defaults to laredo.TotalOrder (a PostgreSQL-backed fan-out is totally ordered).
func WithOrderingGuarantee(o laredo.OrderingGuarantee) Option {
	return func(c *config) { c.ordering = o }
}

// Source is a laredo.SyncSource backed by an upstream laredo fan-out.
type Source struct {
	cfg    config
	table  laredo.TableIdentifier
	client *clientfanout.Client

	mu        sync.Mutex
	queue     []changeRec
	lastAcked string
	state     laredo.SourceState
	signal    chan struct{}
}

type changeRec struct {
	old, new laredo.Row
	position string
}

var _ laredo.SyncSource = (*Source)(nil)

// New creates a fan-out Source.
func New(opts ...Option) *Source {
	cfg := config{cmp: compareLSN, ordering: laredo.TotalOrder, readyTimeout: 30 * time.Second}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Source{
		cfg:    cfg,
		table:  laredo.Table(cfg.schema, cfg.table),
		state:  laredo.SourceConnecting,
		signal: make(chan struct{}, 1),
	}
}

// enqueue records a change. It is called from the client's listener while the
// client holds its lock, so it must not block: a quick mutex append plus a
// non-blocking signal.
func (s *Source) enqueue(old, new laredo.Row, position string) {
	s.mu.Lock()
	s.queue = append(s.queue, changeRec{old: old, new: new, position: position})
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// Init connects to the upstream fan-out, loads its snapshot, and returns the
// table's columns. The change listener is registered before the snapshot is
// awaited so no change is missed between Baseline and Stream.
func (s *Source) Init(ctx context.Context, _ laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
	s.client = clientfanout.New(
		clientfanout.ServerAddress(s.cfg.serverAddress),
		clientfanout.Table(s.cfg.schema, s.cfg.table),
		clientfanout.ClientID(s.cfg.clientID),
	)
	s.client.ListenWithPosition(s.enqueue)
	if err := s.client.Start(ctx); err != nil {
		return nil, fmt.Errorf("fanout source: start: %w", err)
	}
	if !s.client.AwaitReady(s.cfg.readyTimeout) {
		return nil, fmt.Errorf("fanout source: upstream %s not ready within %s", s.cfg.serverAddress, s.cfg.readyTimeout)
	}
	s.setState(laredo.SourceConnected)
	return map[laredo.TableIdentifier][]laredo.ColumnDefinition{s.table: s.client.Columns()}, nil
}

// ValidateTables checks the requested tables are the one this source serves.
func (s *Source) ValidateTables(_ context.Context, tables []laredo.TableIdentifier) []laredo.ValidationError {
	var errs []laredo.ValidationError
	for i := range tables {
		t := tables[i]
		if t != s.table {
			errs = append(errs, laredo.ValidationError{
				Table:   &t,
				Code:    "TABLE_NOT_SERVED",
				Message: fmt.Sprintf("fanout source serves only %s", s.table),
			})
		}
	}
	return errs
}

// Baseline emits the snapshot the client loaded and returns the position it
// reflects. Changes after this position are delivered by Stream.
func (s *Source) Baseline(_ context.Context, _ []laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
	for _, row := range s.client.All() {
		rowCallback(s.table, row)
	}
	pos := s.client.LastSourcePosition()
	s.mu.Lock()
	s.lastAcked = pos
	s.mu.Unlock()
	return pos, nil
}

// Stream forwards changes after `from` to the handler, blocking until ctx is
// cancelled. Changes are buffered from connect time, so any that arrived during
// Baseline are replayed here; those at or before `from` are already in the
// baseline and skipped.
func (s *Source) Stream(ctx context.Context, from laredo.Position, handler laredo.ChangeHandler) error {
	fromStr, _ := from.(string)
	s.setState(laredo.SourceStreaming)
	for {
		s.mu.Lock()
		pending := s.queue
		s.queue = nil
		s.mu.Unlock()

		for _, rec := range pending {
			if fromStr != "" && s.cfg.cmp(rec.position, fromStr) <= 0 {
				continue // already covered by the baseline
			}
			if err := handler.OnChange(s.toEvent(rec)); err != nil {
				return err
			}
			s.mu.Lock()
			s.lastAcked = rec.position
			s.mu.Unlock()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.signal:
		}
	}
}

func (s *Source) toEvent(rec changeRec) laredo.ChangeEvent {
	ev := laredo.ChangeEvent{Table: s.table, Position: rec.position, Timestamp: time.Now()}
	switch {
	case rec.old == nil && rec.new == nil:
		ev.Action = laredo.ActionTruncate
	case rec.old == nil:
		ev.Action = laredo.ActionInsert
		ev.NewValues = rec.new
	case rec.new == nil:
		ev.Action = laredo.ActionDelete
		ev.OldValues = rec.old
	default:
		ev.Action = laredo.ActionUpdate
		ev.OldValues = rec.old
		ev.NewValues = rec.new
	}
	return ev
}

// Ack records that changes up to position are durably processed.
func (s *Source) Ack(_ context.Context, position laredo.Position) error {
	if p, ok := position.(string); ok {
		s.mu.Lock()
		s.lastAcked = p
		s.mu.Unlock()
	}
	return nil
}

// SupportsResume reports that this source can resume from an ACKed position
// (the upstream fan-out resumes by source position).
func (s *Source) SupportsResume() bool { return true }

// LastAckedPosition returns the last ACKed position, or nil if none.
func (s *Source) LastAckedPosition(_ context.Context) (laredo.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastAcked == "" {
		return nil, nil
	}
	return s.lastAcked, nil
}

// ComparePositions orders two positions using the configured comparator.
func (s *Source) ComparePositions(a, b laredo.Position) int {
	as, _ := a.(string)
	bs, _ := b.(string)
	return s.cfg.cmp(as, bs)
}

// PositionToString serializes a position (positions are already strings).
func (s *Source) PositionToString(p laredo.Position) string {
	ps, _ := p.(string)
	return ps
}

// PositionFromString deserializes a position (positions are already strings).
func (s *Source) PositionFromString(str string) (laredo.Position, error) { return str, nil }

// Pause is best-effort: it marks the source paused. The underlying client keeps
// its connection; streaming resumes on Resume. (A future revision may stop the
// upstream stream while paused.)
func (s *Source) Pause(_ context.Context) error {
	s.setState(laredo.SourcePaused)
	return nil
}

// Resume returns the source to streaming after a Pause.
func (s *Source) Resume(_ context.Context) error {
	s.setState(laredo.SourceStreaming)
	return nil
}

// GetLag returns lag information. The fan-out source does not currently expose a
// byte/time lag from the upstream; this returns a zero value.
func (s *Source) GetLag() laredo.LagInfo { return laredo.LagInfo{} }

// OrderingGuarantee returns the configured ordering guarantee (default TotalOrder).
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return s.cfg.ordering }

// State returns the current source state.
func (s *Source) State() laredo.SourceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Close shuts down the upstream client.
func (s *Source) Close(_ context.Context) error {
	if s.client != nil {
		s.client.Stop()
	}
	s.setState(laredo.SourceClosed)
	return nil
}

func (s *Source) setState(st laredo.SourceState) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}
