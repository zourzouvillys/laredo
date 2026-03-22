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

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
)

// Client connects to a fan-out replication service and maintains a local
// in-memory replica of the table.
type Client struct {
	cfg config

	mu           sync.RWMutex
	store        map[string]laredo.Row
	ready        bool
	lastSeq      int64
	lastReceived time.Time
	listener     func(old, new laredo.Row)

	cancel context.CancelFunc
	done   chan struct{}
}

type config struct {
	serverAddress     string
	schema            string
	table             string
	clientID          string
	localSnapshotPath string
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

// New creates a new fan-out client.
func New(opts ...Option) *Client {
	cfg := config{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Client{
		cfg:   cfg,
		store: make(map[string]laredo.Row),
		done:  make(chan struct{}),
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

// LastSequence returns the last received journal sequence number.
func (c *Client) LastSequence() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastSeq
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

func (c *Client) run(ctx context.Context) error {
	rpcClient := replicationv1connect.NewLaredoReplicationServiceClient(
		http.DefaultClient,
		"http://"+c.cfg.serverAddress,
	)

	c.mu.RLock()
	lastSeq := c.lastSeq
	c.mu.RUnlock()

	stream, err := rpcClient.Sync(ctx, connect.NewRequest(&v1.SyncRequest{
		Schema:            c.cfg.schema,
		Table:             c.cfg.table,
		ClientId:          c.cfg.clientID,
		LastKnownSequence: lastSeq,
	}))
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	defer func() { _ = stream.Close() }()

	for stream.Receive() {
		c.mu.Lock()
		c.lastReceived = time.Now()
		c.mu.Unlock()

		msg := stream.Msg()
		switch m := msg.GetMessage().(type) {
		case *v1.SyncResponse_Handshake:
			_ = m

		case *v1.SyncResponse_SnapshotBegin:
			// Clear local state for full snapshot.
			c.mu.Lock()
			c.store = make(map[string]laredo.Row)
			c.mu.Unlock()

		case *v1.SyncResponse_SnapshotRow:
			if m.SnapshotRow.GetRow() != nil {
				row := laredo.Row(m.SnapshotRow.GetRow().AsMap())
				key := rowKey(row)
				c.mu.Lock()
				c.store[key] = row
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
		}
	}

	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("stream error: %w", err)
	}
	return ctx.Err()
}

func (c *Client) applyJournalEntry(entry *v1.ReplicationJournalEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastSeq = entry.GetSequence()

	switch entry.GetAction() {
	case "INSERT":
		if entry.GetNewValues() != nil {
			row := laredo.Row(entry.GetNewValues().AsMap())
			key := rowKey(row)
			c.store[key] = row
			c.notify(nil, row)
		}
	case "UPDATE":
		if entry.GetNewValues() != nil {
			row := laredo.Row(entry.GetNewValues().AsMap())
			key := rowKey(row)
			old := c.store[key]
			c.store[key] = row
			c.notify(old, row)
		}
	case "DELETE":
		if entry.GetOldValues() != nil {
			row := laredo.Row(entry.GetOldValues().AsMap())
			key := rowKey(row)
			old := c.store[key]
			delete(c.store, key)
			c.notify(old, nil)
		}
	case "TRUNCATE":
		c.store = make(map[string]laredo.Row)
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
}

func rowKey(row laredo.Row) string {
	if id, ok := row["id"]; ok {
		return fmt.Sprintf("%v", id)
	}
	// Fallback: use all values concatenated.
	return fmt.Sprintf("%v", row)
}

// localSnapshot is the on-disk format for the client's saved state.
type localSnapshot struct {
	LastSeq int64                 `json:"last_seq"`
	Rows    map[string]laredo.Row `json:"rows"`
}

func (c *Client) saveSnapshot() {
	c.mu.RLock()
	snap := localSnapshot{
		LastSeq: c.lastSeq,
		Rows:    make(map[string]laredo.Row, len(c.store)),
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
	c.mu.Unlock()
}
