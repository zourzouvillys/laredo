// Package fanout provides a Go client for the replication fan-out protocol.
// It connects to a laredo fan-out service via the Sync RPC, receives an
// initial snapshot or delta, then streams live changes to maintain a local
// in-memory replica of the table.
package fanout

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
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
	lastError          error
	connected          bool
	columns            []laredo.ColumnDefinition

	// Listeners are keyed by id so that several may coexist and each
	// unsubscribe removes only its own. A single field meant a second
	// subscriber silently replaced the first, and the first's unsubscribe
	// then removed the second's.
	listeners      map[uint64]func(old, new laredo.Row)
	posListeners   map[uint64]func(old, new laredo.Row, position string)
	snapListeners  map[uint64]func(rows map[string]laredo.Row, position string)
	nextListenerID uint64

	// pkColumns is the primary key as declared in the handshake, in ordinal
	// order. It is how a row's identity is derived; see (*Client).rowKey.
	pkColumns []string

	// pending accumulates a snapshot in progress. Rows are applied here and
	// swapped into store as one assignment at SnapshotEnd, so a reader never
	// observes the replica empty or half-filled.
	pending map[string]laredo.Row

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
	ackInterval       time.Duration
	generation        func() string
	httpClient        connect.HTTPClient
	clientOpts        []connect.ClientOption
	scheme            string
}

// defaultH2CClient speaks HTTP/2 over a plaintext connection. The default used
// to be http.DefaultClient, which negotiates HTTP/1.1 — fine for a
// server-streaming call, but Connect carries bidirectional streams over
// HTTP/2 only, and Sync is now bidirectional so the client can acknowledge
// what it has applied.
var defaultH2CClient = &http.Client{
	Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	},
}

// defaultAckInterval is how often the applied position is reported when the
// caller does not choose. Frequent enough that an operator waiting on a
// configuration change is not left guessing, cheap enough to ignore.
const defaultAckInterval = 1 * time.Second

// Option configures the fan-out client.
type Option func(*config)

// AckInterval sets how often the client reports its applied position to the
// server (default 1s). The report is what lets an operator see that a change
// has been applied rather than merely sent; a shorter interval converges
// faster at the cost of one small message per interval per subscriber.
func AckInterval(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.ackInterval = d
		}
	}
}

// WithHTTPClient supplies the HTTP client used for the Sync stream, which is
// how a caller attaches credentials, TLS, or a custom transport. The client
// previously hardcoded http.DefaultClient and an http:// scheme, so there was
// no way to authenticate to a server that required it, and no way to reach one
// over TLS at all.
//
// A client supplied here must speak HTTP/2: Sync is bidirectional, and Connect
// carries bidirectional streams over HTTP/2 only.
func WithHTTPClient(hc connect.HTTPClient) Option {
	return func(c *config) { c.httpClient = hc }
}

// WithClientOptions passes Connect client options through — interceptors, in
// particular, which is where a per-request credential belongs so that it is
// re-read on every reconnect rather than captured once at construction.
func WithClientOptions(opts ...connect.ClientOption) Option {
	return func(c *config) { c.clientOpts = append(c.clientOpts, opts...) }
}

// WithTLS makes the client dial https:// rather than http://.
func WithTLS() Option {
	return func(c *config) { c.scheme = "https://" }
}

// AppliedGeneration supplies an opaque identifier for the state the consumer
// currently holds — a content hash, typically. It rides on each ack, so a
// caller can confirm that two subscribers hold the same thing rather than
// merely that both reached the same position.
func AppliedGeneration(fn func() string) Option {
	return func(c *config) { c.generation = fn }
}

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
	cfg := config{ackInterval: defaultAckInterval}
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

// Listen registers a change listener. For inserts, old is nil. For deletes,
// new is nil. Returns an unsubscribe function that removes only this listener.
//
// The callback runs after the client has released its lock, so it may read
// the client freely — Get, All and Count are all safe from inside it. It still
// runs on the stream goroutine, so blocking in it stalls replication.
func (c *Client) Listen(fn func(old, new laredo.Row)) func() {
	c.mu.Lock()
	id := c.nextListenerID
	c.nextListenerID++
	if c.listeners == nil {
		c.listeners = make(map[uint64]func(old, new laredo.Row))
	}
	c.listeners[id] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.listeners, id)
		c.mu.Unlock()
	}
}

// OnSnapshotComplete registers a callback fired once a full snapshot has been
// received and installed, with the complete row set and the source position it
// corresponds to.
//
// A consumer maintaining derived state needs this: a re-snapshot replaces the
// whole replica, and reporting it as a stream of individual changes would be
// both misleading and enormous. Between SnapshotBegin and SnapshotEnd the
// client keeps serving the previous contents, so this callback is the point at
// which the new state becomes visible.
func (c *Client) OnSnapshotComplete(fn func(rows map[string]laredo.Row, position string)) func() {
	c.mu.Lock()
	id := c.nextListenerID
	c.nextListenerID++
	if c.snapListeners == nil {
		c.snapListeners = make(map[uint64]func(rows map[string]laredo.Row, position string))
	}
	c.snapListeners[id] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.snapListeners, id)
		c.mu.Unlock()
	}
}

// ListenWithPosition registers a change listener that also receives the source
// position (e.g. WAL LSN) of each change. Conventions match Listen (insert →
// old nil; delete → new nil; truncate → both nil), including that the callback
// runs after the lock is released. Returns an unsubscribe function that removes
// only this listener.
func (c *Client) ListenWithPosition(fn func(old, new laredo.Row, position string)) func() {
	c.mu.Lock()
	id := c.nextListenerID
	c.nextListenerID++
	if c.posListeners == nil {
		c.posListeners = make(map[uint64]func(old, new laredo.Row, position string))
	}
	c.posListeners[id] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.posListeners, id)
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

// goAwayRedialDelay bounds how long a client waits before answering a drain.
const goAwayRedialDelay = 2 * time.Second

// staleAfter is how long without any message — including heartbeats, which the
// server sends on an idle connection — before the replica is considered stale.
// The server's default heartbeat interval is 5s, so this allows several to go
// missing before saying so.
const staleAfter = 30 * time.Second

// LastError returns the most recent stream failure, or nil if the client has
// not failed since it last connected successfully. Reconnection is automatic,
// so this is diagnostic rather than actionable — but without it a consumer had
// no way to tell a healthy quiet table from a client that had been failing to
// connect since startup.
func (c *Client) LastError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastError
}

// Connected reports whether a Sync stream is currently established.
func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// LastReceived returns when the client last received any message from the
// server, heartbeats included. Zero before the first message.
func (c *Client) LastReceived() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastReceived
}

// IsStale reports whether the replica may be behind: nothing has arrived for
// longer than staleAfter, or the client has never received anything at all.
//
// This is the question a consumer actually needs answered before trusting the
// data — the client tracked the timestamp it needs to answer it but exposed no
// way to ask, and read the timestamp nowhere itself.
func (c *Client) IsStale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastReceived.IsZero() {
		return true
	}
	return time.Since(c.lastReceived) > staleAfter
}

func (c *Client) setLastError(err error) {
	c.mu.Lock()
	c.lastError = err
	c.connected = false
	c.mu.Unlock()
}

func (c *Client) setConnected() {
	c.mu.Lock()
	c.connected = true
	c.lastError = nil
	c.mu.Unlock()
}

// columnsFromProto converts replication column definitions to laredo's.
// primaryKeyColumns returns the declared primary-key column names in
// primary-key ordinal order, which is the order the server joins them in when
// it builds a row's key. Empty when the table declares no key.
func primaryKeyColumns(cols []*v1.ColumnDefinition) []string {
	type pk struct {
		name    string
		ordinal int
	}
	var keys []pk
	for _, col := range cols {
		if col.GetIsPrimaryKey() {
			keys = append(keys, pk{col.GetColumnName(), int(col.GetPrimaryKeyOrdinal())})
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ordinal < keys[j].ordinal })
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.name
	}
	return out
}

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
	const (
		baseBackoff = 1 * time.Second
		maxBackoff  = 30 * time.Second
	)
	backoff := baseBackoff

	for {
		started := time.Now()
		err := c.run(ctx)
		if ctx.Err() != nil {
			return // Clean shutdown.
		}
		if err == nil {
			return
		}
		c.setLastError(err)

		// Reset the backoff after a connection that actually did some work.
		// It used to be declared outside this loop and never reset, so a
		// client that saw a few transient failures early on waited the full
		// 30s before every subsequent reconnect for the rest of its life,
		// however long it had been healthy in between.
		if time.Since(started) >= healthyRunDuration {
			backoff = baseBackoff
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// healthyRunDuration is how long a stream must last before its failure is
// treated as a fresh problem rather than a continuation of the last one.
const healthyRunDuration = 30 * time.Second

// jitter spreads reconnects so that a fleet of clients dropped at the same
// moment — every subscriber of a draining server, for instance — does not
// return in lockstep. Full jitter: uniform over [d/2, d].
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1)) //nolint:gosec // spreading reconnects, not a secret
}

// syncStream is the bidirectional Sync stream. The client sends its SyncStart
// and its applied-position acks on it; the server sends replication messages.
type syncStream = connect.BidiStreamForClient[v1.SyncClientMessage, v1.SyncResponse]

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
				_ = stream.CloseRequest()
				_ = stream.CloseResponse()
				return perr
			}
		}

		// Handoff: keep draining the old stream while a new one catches up.
		oldStream := stream
		stopOld, oldDone := c.drainInBackground(oldStream)

		// A drain broadcasts GoAway to every subscriber at once, and this path
		// re-dialled with no delay at all — so the whole fleet came back
		// simultaneously, and if the load balancer routed any of them to
		// another draining task they looped again immediately. Spread them.
		select {
		case <-ctx.Done():
			c.stopDrain(stopOld, oldDone, oldStream)
			return ctx.Err()
		case <-time.After(jitter(goAwayRedialDelay)):
		}

		newStream, derr := c.dial(ctx, true)
		if derr != nil {
			c.stopDrain(stopOld, oldDone, oldStream)
			return derr
		}

		sawGoAway, cerr := c.catchUp(ctx, newStream)

		// New stream is caught up (or failed/handed off again) — drop the old.
		c.stopDrain(stopOld, oldDone, oldStream)

		if cerr != nil {
			_ = newStream.CloseRequest()
			_ = newStream.CloseResponse()
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
	hc := c.cfg.httpClient
	if hc == nil {
		hc = defaultH2CClient
	}
	scheme := c.cfg.scheme
	if scheme == "" {
		scheme = "http://"
	}
	// The gRPC protocol, not Connect's own. Connect's streaming protocol is
	// half-duplex: the server may not send until the client has finished
	// sending, which never happens here because the client holds the request
	// open to acknowledge what it has applied. Under Connect that deadlocks
	// on the first message. gRPC is full-duplex and is what a bidirectional
	// stream needs. Caller options come after, so a caller may still override.
	opts := append([]connect.ClientOption{connect.WithGRPC()}, c.cfg.clientOpts...)
	rpcClient := replicationv1connect.NewLaredoReplicationServiceClient(
		hc, scheme+c.cfg.serverAddress, opts...,
	)

	c.mu.RLock()
	start := &v1.SyncStart{
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
		start.LastKnownSequence = 0
	}

	stream := rpcClient.Sync(ctx)
	if err := stream.Send(&v1.SyncClientMessage{
		Message: &v1.SyncClientMessage_Start{Start: start},
	}); err != nil {
		return nil, fmt.Errorf("sync: send start: %w", err)
	}
	return stream, nil
}

// sendAck reports the client's applied position back to the server. Errors are
// returned but not fatal to the caller: an ack that does not land costs the
// operator visibility, not correctness of the replica.
func (c *Client) sendAck(stream *syncStream) error {
	c.mu.RLock()
	ack := &v1.ApplyAck{
		AppliedSequence:       c.lastSeq,
		AppliedSourcePosition: c.lastSourcePosition,
	}
	gen := c.cfg.generation
	c.mu.RUnlock()
	if gen != nil {
		ack.AppliedGeneration = gen()
	}
	return stream.Send(&v1.SyncClientMessage{Message: &v1.SyncClientMessage_Ack{Ack: ack}})
}

// ackLoop periodically reports the applied position for as long as the stream
// lives. It is the only sender after the initial SyncStart, so no two
// goroutines ever call Send on the same stream.
func (c *Client) ackLoop(ctx context.Context, stream *syncStream, stop <-chan struct{}) {
	t := time.NewTicker(c.cfg.ackInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
			if err := c.sendAck(stream); err != nil {
				return
			}
		}
	}
}

// process reads and applies messages from the primary stream until it ends, the
// context is cancelled, or the server sends GoAway. On GoAway it returns
// (true, nil) with the stream still open so the caller can keep draining it
// during handoff.
func (c *Client) process(ctx context.Context, stream *syncStream) (goAway bool, err error) {
	stop := make(chan struct{})
	go c.ackLoop(ctx, stream, stop)
	defer close(stop)

	// Receive on a bidirectional stream does not observe context
	// cancellation, so closing the stream is what unblocks it. Without this,
	// Stop() waits forever on a client whose server is holding the stream
	// open — which is the normal, healthy case.
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.CloseRequest()
			_ = stream.CloseResponse()
		case <-stop:
		}
	}()

	for {
		msg, rerr := stream.Receive()
		if rerr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if errors.Is(rerr, io.EOF) {
				return false, nil
			}
			return false, fmt.Errorf("stream error: %w", rerr)
		}
		c.touch()
		if c.applyMessage(msg) {
			return true, nil
		}
	}
}

// catchUp reads and applies messages from a freshly dialed stream until it has
// delivered everything the new instance had at connect time (its handshake
// ServerCurrentSequence) — at which point the old stream can be dropped — or
// until the stream ends/errors or sends its own GoAway. It leaves the stream
// open for continued processing as the new primary.
func (c *Client) catchUp(ctx context.Context, stream *syncStream) (sawGoAway bool, err error) {
	var target int64
	var haveTarget bool
	for {
		msg, rerr := stream.Receive()
		if rerr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if errors.Is(rerr, io.EOF) {
				return false, nil
			}
			return false, fmt.Errorf("stream error: %w", rerr)
		}
		c.touch()
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
			msg, err := stream.Receive()
			if err != nil {
				return
			}
			c.touch()
			// Ignore GoAway on the old stream: we are already leaving it.
			_ = c.applyMessage(msg)
		}
	}()
	return stop, done
}

// stopDrain tears down a background drainer started by drainInBackground.
// Closing the stream is what unblocks a goroutine parked in Receive(); the stop
// channel only guards against applying further messages between iterations.
func (c *Client) stopDrain(stop, done chan struct{}, stream *syncStream) {
	close(stop)
	_ = stream.CloseRequest()
	_ = stream.CloseResponse()
	<-done
}

// applyMessage applies a single Sync message to the local replica. It returns
// true if the message is a GoAway (the server is draining).
func (c *Client) applyMessage(msg *v1.SyncResponse) (goAway bool) {
	switch m := msg.GetMessage().(type) {
	case *v1.SyncResponse_Handshake:
		c.setConnected()
		if cols := m.Handshake.GetColumns(); len(cols) > 0 {
			c.mu.Lock()
			c.columns = columnsFromProto(cols)
			c.pkColumns = primaryKeyColumns(cols)
			c.mu.Unlock()
		}

	case *v1.SyncResponse_SnapshotBegin:
		// Start accumulating into a fresh map rather than clearing the live
		// one. A re-snapshot on reconnect used to wipe the replica in place
		// while ready stayed true, so AwaitReady kept passing and concurrent
		// readers saw the table empty and then refill row by row.
		c.mu.Lock()
		c.pending = make(map[string]laredo.Row, len(c.store))
		c.lastSnapshotID = m.SnapshotBegin.GetSnapshotId()
		c.mu.Unlock()

	case *v1.SyncResponse_SnapshotRow:
		if m.SnapshotRow.GetRow() != nil {
			row := laredo.Row(m.SnapshotRow.GetRow().AsMap())
			c.mu.Lock()
			if c.pending == nil {
				// A row outside a Begin/End pair; tolerate it rather than
				// panicking, treating it as the start of a snapshot.
				c.pending = make(map[string]laredo.Row)
			}
			c.pending[c.rowKeyLocked(row)] = row
			c.mu.Unlock()
		}

	case *v1.SyncResponse_SnapshotEnd:
		c.mu.Lock()
		if c.pending != nil {
			c.store = c.pending
			c.pending = nil
			c.rebuildIndexes()
			// The snapshot supersedes any position carried over from a
			// previous connection; subsequent journal entries repopulate it.
			c.lastSourcePosition = ""
		}
		c.ready = true
		c.lastSeq = m.SnapshotEnd.GetSequence()
		rows := make(map[string]laredo.Row, len(c.store))
		for k, v := range c.store {
			rows[k] = v
		}
		pos := c.lastSourcePosition
		snapFns := make([]func(map[string]laredo.Row, string), 0, len(c.snapListeners))
		for _, fn := range c.snapListeners {
			snapFns = append(snapFns, fn)
		}
		c.mu.Unlock()
		for _, fn := range snapFns {
			fn(rows, pos)
		}

	case *v1.SyncResponse_JournalEntry:
		c.applyJournalEntry(m.JournalEntry)

	case *v1.SyncResponse_Heartbeat:
		// A heartbeat is not just a liveness ping: it carries the position the
		// server has reached, which is how an idle or heavily filtered
		// subscriber stays current instead of drifting arbitrarily far behind
		// the tail and forcing a re-snapshot on its next resume.
		if pos := m.Heartbeat.GetSourcePosition(); pos != "" {
			c.mu.Lock()
			c.lastSourcePosition = pos
			c.mu.Unlock()
		}

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
	// notifyOld/notifyNew describe the change to hand to listeners once the
	// lock is released. Calling them while holding c.mu deadlocked any
	// listener that read the client back, because sync.RWMutex is not
	// reentrant and the accessors all take RLock.
	var notifyOld, notifyNew laredo.Row
	var notifyDo bool

	c.mu.Lock()

	c.lastSeq = entry.GetSequence()
	if pos := entry.GetSourcePosition(); pos != "" {
		c.lastSourcePosition = pos
	}

	switch entry.GetAction() {
	case "INSERT":
		if entry.GetNewValues() != nil {
			row := laredo.Row(entry.GetNewValues().AsMap())
			key := c.rowKeyLocked(row)
			c.store[key] = row
			c.addToIndexes(key, row)
			notifyOld, notifyNew, notifyDo = nil, row, true
		}
	case "UPDATE":
		if entry.GetNewValues() != nil {
			row := laredo.Row(entry.GetNewValues().AsMap())
			key := c.rowKeyLocked(row)
			old := c.store[key]
			if old != nil {
				c.removeFromIndexes(key, old)
			}
			c.store[key] = row
			c.addToIndexes(key, row)
			notifyOld, notifyNew, notifyDo = old, row, true
		}
	case "DELETE":
		if entry.GetOldValues() != nil {
			row := laredo.Row(entry.GetOldValues().AsMap())
			key := c.rowKeyLocked(row)
			old := c.store[key]
			if old != nil {
				c.removeFromIndexes(key, old)
			}
			delete(c.store, key)
			notifyOld, notifyNew, notifyDo = old, nil, true
		}
	case "TRUNCATE":
		c.store = make(map[string]laredo.Row)
		c.clearIndexes()
		notifyOld, notifyNew, notifyDo = nil, nil, true
	}

	// Mark ready after first journal entry if not already ready (delta mode).
	if !c.ready {
		c.ready = true
	}

	// Copy the listener sets and the position before unlocking, so the
	// delivery below is not racing a concurrent Listen or unsubscribe.
	pos := c.lastSourcePosition
	var fns []func(laredo.Row, laredo.Row)
	var posFns []func(laredo.Row, laredo.Row, string)
	if notifyDo {
		fns = make([]func(laredo.Row, laredo.Row), 0, len(c.listeners))
		for _, fn := range c.listeners {
			fns = append(fns, fn)
		}
		posFns = make([]func(laredo.Row, laredo.Row, string), 0, len(c.posListeners))
		for _, fn := range c.posListeners {
			posFns = append(posFns, fn)
		}
	}
	c.mu.Unlock()

	for _, fn := range fns {
		fn(notifyOld, notifyNew)
	}
	for _, fn := range posFns {
		fn(notifyOld, notifyNew, pos)
	}
}

// rowKeyLocked derives a row's identity. Caller must hold c.mu.
//
// The key must match how the server identifies the row, which is by the
// primary-key columns it declares in the handshake. Assuming a column called
// "id" was wrong for every table keyed on anything else: the fallback below
// stringifies the whole row, so any column changing produced a *different*
// key and an UPDATE inserted a duplicate instead of replacing the original.
func (c *Client) rowKeyLocked(row laredo.Row) string {
	if len(c.pkColumns) > 0 {
		if len(c.pkColumns) == 1 {
			return fmt.Sprintf("%v", row[c.pkColumns[0]])
		}
		key := ""
		for i, col := range c.pkColumns {
			if i > 0 {
				key += "\x00"
			}
			key += fmt.Sprintf("%v", row[col])
		}
		return key
	}
	// No declared key (an older server, or a table without one): fall back to
	// a conventional "id", then to the whole row.
	if id, ok := row["id"]; ok {
		return fmt.Sprintf("%v", id)
	}
	return fmt.Sprintf("%v", row)
}

// rebuildIndexes recomputes every secondary index from the current store.
// Caller must hold c.mu.
func (c *Client) rebuildIndexes() {
	c.clearIndexes()
	for k, row := range c.store {
		c.addToIndexes(k, row)
	}
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
