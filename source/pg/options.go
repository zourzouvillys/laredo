package pg

import "time"

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
}

// ReconnectConfig controls reconnection behavior on transient failures.
type ReconnectConfig struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64
}

// Option configures a PostgreSQL source.
type Option func(*sourceConfig)

type sourceConfig struct {
	connString  string
	slotMode    SlotMode
	slotName    string
	publication PublicationConfig
	reconnect   ReconnectConfig
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
