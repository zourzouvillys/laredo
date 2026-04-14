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
	connString    string
	slotMode      SlotMode
	slotName      string
	publication   PublicationConfig
	reconnect     ReconnectConfig
	beforeConnect BeforeConnectHook
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
