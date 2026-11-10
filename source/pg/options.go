package pg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// SlotMode controls replication slot lifecycle.
type SlotMode int

// Slot modes.
const (
	// SlotEphemeral creates a temporary replication slot that is dropped on
	// disconnect. Requires a full baseline every startup. SupportsResume()
	// returns false.
	SlotEphemeral SlotMode = iota

	// SlotStateful uses a persistent named replication slot. The source can
	// resume from the last ACKed LSN after restart. SupportsResume() returns true.
	SlotStateful
)

// PublicationConfig controls publication management.
type PublicationConfig struct {
	// Name is the publication name. Defaults to "{slot_name}_pub".
	Name string

	// Create controls whether the source creates the publication on startup.
	// If false, the publication must already exist.
	Create bool

	// TableOptions provides per-table publication settings (PostgreSQL 15+).
	// Key is "schema.table". If a table is not listed, it is added without
	// row filters or column lists.
	TableOptions map[string]TablePublicationConfig
}

// TablePublicationConfig controls per-table publication settings (PostgreSQL 15+).
type TablePublicationConfig struct {
	// RowFilter is a SQL WHERE clause for row filtering (e.g., "id > 0").
	// Empty means no filter (all rows published).
	RowFilter string

	// Columns restricts the publication to specific columns.
	// Empty means all columns are published.
	Columns []string
}

// ReconnectConfig controls reconnection behavior on transient failures.
type ReconnectConfig struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64
}

// withDefaults fills any zero field with its default, so a partial
// Reconnect(...) option keeps the defaults for the fields it leaves out.
func (r ReconnectConfig) withDefaults() ReconnectConfig {
	d := defaultConfig().reconnect
	if r.MaxAttempts == 0 {
		r.MaxAttempts = d.MaxAttempts
	}
	if r.InitialBackoff == 0 {
		r.InitialBackoff = d.InitialBackoff
	}
	if r.MaxBackoff == 0 {
		r.MaxBackoff = d.MaxBackoff
	}
	if r.Multiplier == 0 {
		r.Multiplier = d.Multiplier
	}
	return r
}

// grow returns the backoff to wait after b, capped at MaxBackoff.
func (r ReconnectConfig) grow(b time.Duration) time.Duration {
	b = time.Duration(float64(b) * r.Multiplier)
	if b > r.MaxBackoff {
		b = r.MaxBackoff
	}
	return b
}

// backoffAfter returns the wait before retry n (1-based): InitialBackoff,
// grown n-1 times.
func (r ReconnectConfig) backoffAfter(n int) time.Duration {
	b := r.InitialBackoff
	for i := 1; i < n; i++ {
		b = r.grow(b)
	}
	return b
}

// BeforeConnectHook is invoked immediately before each PostgreSQL connection
// is established. It receives the parsed *pgconn.Config and may mutate it —
// typically to compute a short-lived password (for example an AWS RDS IAM
// auth token) or adjust host/port for topology-aware routing.
//
// The hook runs for both the query connection (baseline SELECTs, schema
// discovery) and the replication connection (logical replication stream).
// Implementations can distinguish the two via
// cfg.RuntimeParams["replication"] — "database" is set on the replication
// connection, the query connection has no such entry.
//
// Returning an error aborts connection setup. The hook must be safe to
// call from multiple goroutines if reconnection is enabled.
//
// The hook is intentionally minimal and cloud-agnostic so laredo itself
// has no dependency on any specific auth provider.
type BeforeConnectHook func(ctx context.Context, cfg *pgconn.Config) error

// Option configures a PostgreSQL source.
type Option func(*sourceConfig)

type sourceConfig struct {
	connString     string
	slotMode       SlotMode
	slotName       string
	publication    PublicationConfig
	reconnect      ReconnectConfig
	beforeConnect  BeforeConnectHook
	alwaysBaseline bool
}

func defaultConfig() sourceConfig {
	return sourceConfig{
		slotMode: SlotEphemeral,
		slotName: "laredo_slot",
		publication: PublicationConfig{
			Create: true,
		},
		reconnect: ReconnectConfig{
			MaxAttempts:    10,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     30 * time.Second,
			Multiplier:     2.0,
		},
	}
}

// Connection sets the PostgreSQL connection string.
func Connection(connString string) Option {
	return func(c *sourceConfig) {
		c.connString = connString
	}
}

// SlotModeOpt sets the replication slot mode (ephemeral or stateful).
func SlotModeOpt(mode SlotMode) Option {
	return func(c *sourceConfig) {
		c.slotMode = mode
	}
}

// SlotName sets the replication slot name (default "laredo_slot").
func SlotName(name string) Option {
	return func(c *sourceConfig) {
		c.slotName = name
	}
}

// Publication configures publication management.
func Publication(cfg PublicationConfig) Option {
	return func(c *sourceConfig) {
		c.publication = cfg
	}
}

// Reconnect configures reconnection behavior.
func Reconnect(cfg ReconnectConfig) Option {
	return func(c *sourceConfig) {
		c.reconnect = cfg
	}
}

// BeforeConnect registers a hook invoked just before each connection is
// opened. See BeforeConnectHook for semantics. Pass nil to clear a
// previously-set hook.
func BeforeConnect(h BeforeConnectHook) Option {
	return func(c *sourceConfig) {
		c.beforeConnect = h
	}
}

// AlwaysBaseline forces a full baseline COPY on every startup, even in
// stateful mode where the source could otherwise resume from the last ACKed
// LSN.
//
// Resuming from a persistent slot only reconstructs a target that is durable
// across process restarts. An in-memory target (for example loading config or
// rules into memory) is rebuilt empty on every start, so resuming from the
// last ACKed position would leave it empty and only ever apply changes written
// after the restart. Enable this so such a target is fully repopulated from the
// current table contents on each startup while still keeping a persistent slot
// (so WAL is retained while the process is running).
//
// This has no effect in ephemeral mode, which already baselines every startup.
func AlwaysBaseline(enabled bool) Option {
	return func(c *sourceConfig) {
		c.alwaysBaseline = enabled
	}
}
