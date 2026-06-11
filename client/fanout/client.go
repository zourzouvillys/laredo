// Package fanout provides a Go client for the replication fan-out protocol.
// It connects to a laredo fan-out service via the Sync RPC, receives an
// initial snapshot or delta, then streams live changes to maintain a local
// in-memory replica of the table.
package fanout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
)

// Client connects to a fan-out replication service and maintains a local
// in-memory replica of the table.
type Client struct {
	cfg config

	mu             sync.RWMutex
	store          map[string]laredo.Row
	indexes        map[string]*secondaryIndex // name → index
	ready          bool
	lastSeq        int64
	lastSnapshotID string
	// lastSourcePosition is the source position (e.g. WAL LSN) of the last
	// applied change. Unlike lastSeq it is stable across server instances, so it
	// is what the client resumes from when failing over to another instance.
	lastSourcePosition string
	lastReceived       time.Time
	listener           func(old, new laredo.Row)
	posListener        func(old, new laredo.Row, position string)
	columns            []laredo.ColumnDefinition

	cancel context.CancelFunc
	done   chan struct{}
}

// secondaryIndex maintains a mapping from field values to row keys.
type secondaryIndex struct {
	fields  []string
	entries map[string][]string // index key → row keys
}

type indexConfig struct {
	name   string
	fields []string
}

type config struct {
	serverAddress     string
	schema            string
	table             string
	clientID          string
	localSnapshotPath string
	indexes           []indexConfig
	filters           []*v1.FieldPredicate
}

// Option configures the fan-out client.
type Option func(*config)

// ServerAddress sets the gRPC server address (e.g. "localhost:4002").
func ServerAddress(addr string) Option {
	return func(c *config) { c.serverAddress = addr }
}

// Table sets the table to replicate (schema and table name).
func Table(schema, table string) Option {
	return func(c *config) { c.schema = schema; c.table = table }
}

// ClientID sets the client identifier for monitoring.
func ClientID(id string) Option {
	return func(c *config) { c.clientID = id }
}

// LocalSnapshotPath sets the path for saving/restoring client state.
// On start, if the file exists, the client loads it and requests a delta
// from the server instead of a full snapshot. On stop, the client saves
// its current state so the next start is faster.
func LocalSnapshotPath(path string) Option {
	return func(c *config) { c.localSnapshotPath = path }
}

// WithIndex adds a secondary index for fast lookups by the given fields.
// Use LookupByIndex to query it.
func WithIndex(name string, fields ...string) Option {
	return func(c *config) {
		c.indexes = append(c.indexes, indexConfig{name: name, fields: fields})
	}
}

// WithFilterEquals adds a server-side subscription filter requiring the given
// column to equal value (a JSON scalar: string, number, or bool). The server
// then sends only matching rows and changes, across the snapshot, catch-up, and
// live phases — other partitions' rows never cross the wire. This is the common
// partition-scoping case, e.g. WithFilterEquals("tenant_id", "acme"). Multiple
// filter options are AND-combined. Values of unsupported types are ignored.
func WithFilterEquals(field string, value any) Option {
	return func(c *config) {
		v, err := structpb.NewValue(value)
		if err != nil {
			return
		}
		c.filters = append(c.filters, &v1.FieldPredicate{
			Field: field,
			Match: &v1.FieldPredicate_Equals{Equals: v},
		})
	}
}

// WithFilterPrefix adds a server-side subscription filter requiring the given
// (string) column to start with prefix. AND-combined with other filters.
func WithFilterPrefix(field, prefix string) Option {
	return func(c *config) {
		c.filters = append(c.filters, &v1.FieldPredicate{
			Field: field,
			Match: &v1.FieldPredicate_Prefix{Prefix: prefix},
		})
	}
}

// WithFilterIn adds a server-side subscription filter requiring the given column
// to equal one of values (JSON scalars). AND-combined with other filters.
// Unsupported value types are ignored.
func WithFilterIn(field string, values ...any) Option {
	return func(c *config) {
		lv, err := structpb.NewList(values)
		if err != nil {
			return
		}
		c.filters = append(c.filters, &v1.FieldPredicate{
			Field: field,
			Match: &v1.FieldPredicate_In{In: lv},
		})
	}
}

// New creates a new fan-out client.
func New(opts ...Option) *Client {
	cfg := config{}
	for _, opt := range opts {
		opt(&cfg)
	}

	indexes := make(map[string]*secondaryIndex, len(cfg.indexes))
	for _, ic := range cfg.indexes {
		indexes[ic.name] = &secondaryIndex{
			fields:  ic.fields,
			entries: make(map[string][]string),
		}
	}

	return &Client{
		cfg:     cfg,
		store:   make(map[string]laredo.Row),
		indexes: indexes,
		done:    make(chan struct{}),
	}
}

// Start connects to the server and begins receiving data.
// Non-blocking — runs in the background. If LocalSnapshotPath is configured
// and the file exists, the client loads it before connecting.
func (c *Client) Start(ctx context.Context) error {
	if c.cfg.localSnapshotPath != "" {
		c.loadSnapshot()
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	go func() {
		defer close(c.done)
		c.runWithReconnect(runCtx)
	}()

	return nil
}

// AwaitReady blocks until the initial state is loaded or the timeout expires.
func (c *Client) AwaitReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.RLock()
		ready := c.ready
		c.mu.RUnlock()
		if ready {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Get returns a row by key, or false if not found.
func (c *Client) Get(key string) (laredo.Row, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	row, ok := c.store[key]
	return row, ok
}

// Count returns the number of rows in the local replica.
func (c *Client) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.store)
}

// Listen registers a change listener. For inserts, old is nil.
// For deletes, new is nil. Returns an unsubscribe function.
func (c *Client) Listen(fn func(old, new laredo.Row)) func() {
	c.mu.Lock()
	c.listener = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.listener = nil
		c.mu.Unlock()
	}
}

// ListenWithPosition registers a change listener that also receives the source
// position (e.g. WAL LSN) of each change. Conventions match Listen (insert →
// old nil; delete → new nil; truncate → both nil). The callback runs while the
// client holds its lock, so it must not block or call back into the client.
// Returns an unsubscribe function.
func (c *Client) ListenWithPosition(fn func(old, new laredo.Row, position string)) func() {
	c.mu.Lock()
	c.posListener = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.posListener = nil
		c.mu.Unlock()
	}
}

// Columns returns the table's column definitions as learned from the server
// handshake. Empty until the client has connected and received a handshake.
func (c *Client) Columns() []laredo.ColumnDefinition {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]laredo.ColumnDefinition(nil), c.columns...)
}

// columnsFromProto converts replication column definitions to laredo's.
func columnsFromProto(cols []*v1.ColumnDefinition) []laredo.ColumnDefinition {
	out := make([]laredo.ColumnDefinition, len(cols))
	for i, col := range cols {
		out[i] = laredo.ColumnDefinition{
			Name:              col.GetColumnName(),
			Type:              col.GetDataType(),
			Nullable:          !col.GetNotNull(),
			PrimaryKey:        col.GetIsPrimaryKey(),
			OrdinalPosition:   int(col.GetOrdinalPosition()),
			PrimaryKeyOrdinal: int(col.GetPrimaryKeyOrdinal()),
		}
	}
	return out
}

// LastSequence returns the last received journal sequence number.
func (c *Client) LastSequence() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastSeq
}

// LastSourcePosition returns the source position (e.g. PostgreSQL WAL LSN) of
// the last applied change. Unlike LastSequence it is stable across server
// instances. Empty until the first change carrying a position is applied.
func (c *Client) LastSourcePosition() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastSourcePosition
}

// Lookup returns a row by looking up a field value. Scans all rows (O(n)).
func (c *Client) Lookup(field string, value any) (laredo.Row, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	valStr := fmt.Sprintf("%v", value)
	for _, row := range c.store {
		if fmt.Sprintf("%v", row[field]) == valStr {
			return row, true
		}
	}
	return nil, false
}

// All returns all rows in the local replica.
func (c *Client) All() []laredo.Row {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rows := make([]laredo.Row, 0, len(c.store))
	for _, row := range c.store {
		rows = append(rows, row)
	}
	return rows
}

// LookupByIndex returns all rows matching the given values on a named secondary index.
// Returns nil if the index doesn't exist or no rows match.
func (c *Client) LookupByIndex(indexName string, values ...any) []laredo.Row {
	c.mu.RLock()
	defer c.mu.RUnlock()

	idx, ok := c.indexes[indexName]
	if !ok {
		return nil
	}

	key := indexKey(idx.fields, values)
	rowKeys := idx.entries[key]
	if len(rowKeys) == 0 {
		return nil
	}

	rows := make([]laredo.Row, 0, len(rowKeys))
	for _, rk := range rowKeys {
		if row, ok := c.store[rk]; ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// Stop disconnects from the server and stops the client.
// If LocalSnapshotPath is configured, saves the current state to disk.
func (c *Client) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done

	if c.cfg.localSnapshotPath != "" {
		c.saveSnapshot()
	}
}

func (c *Client) runWithReconnect(ctx context.Context) {
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		err := c.run(ctx)
		if ctx.Err() != nil {
			return // Clean shutdown.
		}
		if err == nil {
			return
		}

		// Exponential backoff before reconnect.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

type syncStream = connect.ServerStreamForClient[v1.SyncResponse]

// run opens a Sync stream and processes it as the primary. When the server
// sends GoAway (it is draining), run performs an overlapping handoff: it brings
// up a new stream (re-dialing the configured address, which a load balancer
// routes to a healthy instance) and resumes by source position — no full
// re-sync — keeping the old stream live until the new one has caught up, then
// closes the old one cleanly.
func (c *Client) run(ctx context.Context) error {
	stream, err := c.dial(ctx, false)
	if err != nil {
		return err
	}

	pendingGoAway := false
	for {
		if !pendingGoAway {
			goAway, perr := c.process(ctx, stream)
			if perr != nil || !goAway {
				_ = stream.Close()
				return perr
			}
		}

		// Handoff: keep draining the old stream while a new one catches up.
		oldStream := stream
		stopOld, oldDone := c.drainInBackground(oldStream)

		newStream, derr := c.dial(ctx, true)
		if derr != nil {
			c.stopDrain(stopOld, oldDone, oldStream)
			return derr
		}

		sawGoAway, cerr := c.catchUp(ctx, newStream)

		// New stream is caught up (or failed/handed off again) — drop the old.
		c.stopDrain(stopOld, oldDone, oldStream)

		if cerr != nil {
			_ = newStream.Close()
			return cerr
		}

		stream = newStream
		pendingGoAway = sawGoAway
	}
}

// dial opens a new Sync stream. When resumeByPosition is set (a cross-instance
// handoff), it resumes strictly by source position: the per-instance sequence
// is not portable, so it is omitted to avoid matching an unrelated entry on the
// new instance.
func (c *Client) dial(ctx context.Context, resumeByPosition bool) (*syncStream, error) {
	rpcClient := replicationv1connect.NewLaredoReplicationServiceClient(
		http.DefaultClient,
		"http://"+c.cfg.serverAddress,
	)

	c.mu.RLock()
	req := &v1.SyncRequest{
		Schema:                  c.cfg.schema,
		Table:                   c.cfg.table,
		ClientId:                c.cfg.clientID,
		LastKnownSequence:       c.lastSeq,
		LastSnapshotId:          c.lastSnapshotID,
		LastKnownSourcePosition: c.lastSourcePosition,
		Filters:                 c.cfg.filters,
	}
	c.mu.RUnlock()
	if resumeByPosition {
		req.LastKnownSequence = 0
	}

	stream, err := rpcClient.Sync(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, fmt.Errorf("sync: %w", err)
	}
	return stream, nil
}

// process reads and applies messages from the primary stream until it ends, the
// context is cancelled, or the server sends GoAway. On GoAway it returns
// (true, nil) with the stream still open so the caller can keep draining it
// during handoff.
func (c *Client) process(ctx context.Context, stream *syncStream) (goAway bool, err error) {
	for stream.Receive() {
		c.touch()
		if c.applyMessage(stream.Msg()) {
			return true, nil
		}
	}
	if e := stream.Err(); e != nil && ctx.Err() == nil {
		return false, fmt.Errorf("stream error: %w", e)
	}
	return false, ctx.Err()
}

// catchUp reads and applies messages from a freshly dialed stream until it has
// delivered everything the new instance had at connect time (its handshake
// ServerCurrentSequence) — at which point the old stream can be dropped — or
// until the stream ends/errors or sends its own GoAway. It leaves the stream
// open for continued processing as the new primary.
func (c *Client) catchUp(ctx context.Context, stream *syncStream) (sawGoAway bool, err error) {
	var target int64
	var haveTarget bool
	for stream.Receive() {
		c.touch()
		msg := stream.Msg()
		if hs := msg.GetHandshake(); hs != nil {
			target = hs.GetServerCurrentSequence()
			haveTarget = true
		}
		if c.applyMessage(msg) {
			return true, nil
		}
		if haveTarget {
			c.mu.RLock()
			caught := c.lastSeq >= target
			c.mu.RUnlock()
			if caught {
				return false, nil
			}
		}
	}
	if e := stream.Err(); e != nil && ctx.Err() == nil {
		return false, fmt.Errorf("stream error: %w", e)
	}
	return false, ctx.Err()
}

// drainInBackground keeps applying messages from a stream (the old instance,
// during a handoff) until signalled to stop or the stream ends. Idempotent
// application makes the brief overlap with the new stream safe. Returns a stop
// channel (close to stop) and a done channel (closed when the goroutine exits).
func (c *Client) drainInBackground(stream *syncStream) (stop chan struct{}, done chan struct{}) {
	stop = make(chan struct{})
	done = make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if !stream.Receive() {
				return
			}
			c.touch()
			// Ignore GoAway on the old stream: we are already leaving it.
			_ = c.applyMessage(stream.Msg())
		}
	}()
	return stop, done
}

// stopDrain tears down a background drainer started by drainInBackground.
// Closing the stream is what unblocks a goroutine parked in Receive(); the stop
// channel only guards against applying further messages between iterations.
func (c *Client) stopDrain(stop, done chan struct{}, stream *syncStream) {
	close(stop)
	_ = stream.Close()
	<-done
}

// applyMessage applies a single Sync message to the local replica. It returns
// true if the message is a GoAway (the server is draining).
func (c *Client) applyMessage(msg *v1.SyncResponse) (goAway bool) {
	switch m := msg.GetMessage().(type) {
	case *v1.SyncResponse_Handshake:
		if cols := m.Handshake.GetColumns(); len(cols) > 0 {
			c.mu.Lock()
			c.columns = columnsFromProto(cols)
			c.mu.Unlock()
		}

	case *v1.SyncResponse_SnapshotBegin:
		// Clear local state for full snapshot. The source position is reset; it
		// is repopulated from subsequent journal entries.
		c.mu.Lock()
		c.store = make(map[string]laredo.Row)
		c.clearIndexes()
		c.lastSnapshotID = m.SnapshotBegin.GetSnapshotId()
		c.lastSourcePosition = ""
		c.mu.Unlock()

	case *v1.SyncResponse_SnapshotRow:
		if m.SnapshotRow.GetRow() != nil {
			row := laredo.Row(m.SnapshotRow.GetRow().AsMap())
			key := rowKey(row)
			c.mu.Lock()
			c.store[key] = row
			c.addToIndexes(key, row)
			c.mu.Unlock()
		}

	case *v1.SyncResponse_SnapshotEnd:
		c.mu.Lock()
		c.ready = true
		c.lastSeq = m.SnapshotEnd.GetSequence()
		c.mu.Unlock()

	case *v1.SyncResponse_JournalEntry:
		c.applyJournalEntry(m.JournalEntry)

	case *v1.SyncResponse_Heartbeat:
		// Heartbeat — connection is alive.
		_ = m

	case *v1.SyncResponse_SchemaChange:
		// Schema change — could trigger re-baseline.
		_ = m

	case *v1.SyncResponse_GoAway:
		return true
	}
	return false
}

// touch records the time of the last received message.
func (c *Client) touch() {
	c.mu.Lock()
	c.lastReceived = time.Now()
	c.mu.Unlock()
}

func (c *Client) applyJournalEntry(entry *v1.ReplicationJournalEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastSeq = entry.GetSequence()
	if pos := entry.GetSourcePosition(); pos != "" {
		c.lastSourcePosition = pos
	}

	switch entry.GetAction() {
	case "INSERT":
		if entry.GetNewValues() != nil {
			row := laredo.Row(entry.GetNewValues().AsMap())
			key := rowKey(row)
			c.store[key] = row
			c.addToIndexes(key, row)
			c.notify(nil, row)
		}
	case "UPDATE":
		if entry.GetNewValues() != nil {
			row := laredo.Row(entry.GetNewValues().AsMap())
			key := rowKey(row)
			old := c.store[key]
			if old != nil {
				c.removeFromIndexes(key, old)
			}
			c.store[key] = row
			c.addToIndexes(key, row)
			c.notify(old, row)
		}
	case "DELETE":
		if entry.GetOldValues() != nil {
			row := laredo.Row(entry.GetOldValues().AsMap())
			key := rowKey(row)
			old := c.store[key]
			if old != nil {
				c.removeFromIndexes(key, old)
			}
			delete(c.store, key)
			c.notify(old, nil)
		}
	case "TRUNCATE":
		c.store = make(map[string]laredo.Row)
		c.clearIndexes()
		c.notify(nil, nil)
	}

	// Mark ready after first journal entry if not already ready (delta mode).
	if !c.ready {
		c.ready = true
	}
}

func (c *Client) notify(old, new laredo.Row) {
	if c.listener != nil {
		c.listener(old, new)
	}
	if c.posListener != nil {
		// Called under c.mu from applyJournalEntry, where lastSourcePosition is
		// already the position of this change.
		c.posListener(old, new, c.lastSourcePosition)
	}
}

func rowKey(row laredo.Row) string {
	if id, ok := row["id"]; ok {
		return fmt.Sprintf("%v", id)
	}
	// Fallback: use all values concatenated.
	return fmt.Sprintf("%v", row)
}

// addToIndexes adds a row to all secondary indexes. Caller must hold c.mu.
func (c *Client) addToIndexes(rk string, row laredo.Row) {
	for _, idx := range c.indexes {
		vals := make([]any, len(idx.fields))
		for i, f := range idx.fields {
			vals[i] = row[f]
		}
		key := indexKey(idx.fields, vals)
		idx.entries[key] = append(idx.entries[key], rk)
	}
}

// removeFromIndexes removes a row from all secondary indexes. Caller must hold c.mu.
func (c *Client) removeFromIndexes(rk string, row laredo.Row) {
	for _, idx := range c.indexes {
		vals := make([]any, len(idx.fields))
		for i, f := range idx.fields {
			vals[i] = row[f]
		}
		key := indexKey(idx.fields, vals)
		entries := idx.entries[key]
		for i, k := range entries {
			if k == rk {
				idx.entries[key] = append(entries[:i], entries[i+1:]...)
				break
			}
		}
		if len(idx.entries[key]) == 0 {
			delete(idx.entries, key)
		}
	}
}

// clearIndexes empties all secondary indexes. Caller must hold c.mu.
func (c *Client) clearIndexes() {
	for _, idx := range c.indexes {
		idx.entries = make(map[string][]string)
	}
}

// indexKey builds a composite key from field names and values.
func indexKey(_ []string, values []any) string {
	if len(values) == 1 {
		return fmt.Sprintf("%v", values[0])
	}
	key := ""
	for i, v := range values {
		if i > 0 {
			key += "\x00"
		}
		key += fmt.Sprintf("%v", v)
	}
	return key
}

// localSnapshot is the on-disk format for the client's saved state.
type localSnapshot struct {
	LastSeq            int64                 `json:"last_seq"`
	SnapshotID         string                `json:"snapshot_id,omitempty"`
	LastSourcePosition string                `json:"last_source_position,omitempty"`
	Rows               map[string]laredo.Row `json:"rows"`
}

func (c *Client) saveSnapshot() {
	c.mu.RLock()
	snap := localSnapshot{
		LastSeq:            c.lastSeq,
		SnapshotID:         c.lastSnapshotID,
		LastSourcePosition: c.lastSourcePosition,
		Rows:               make(map[string]laredo.Row, len(c.store)),
	}
	for k, v := range c.store {
		snap.Rows[k] = v
	}
	c.mu.RUnlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return // Best effort — don't block stop.
	}
	_ = os.WriteFile(c.cfg.localSnapshotPath, data, 0o600)
}

func (c *Client) loadSnapshot() {
	data, err := os.ReadFile(c.cfg.localSnapshotPath)
	if err != nil {
		return // File doesn't exist yet — normal on first start.
	}

	var snap localSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return // Corrupted — start fresh.
	}

	c.mu.Lock()
	c.store = snap.Rows
	c.lastSeq = snap.LastSeq
	c.lastSnapshotID = snap.SnapshotID
	c.lastSourcePosition = snap.LastSourcePosition
	c.mu.Unlock()
}
