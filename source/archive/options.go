package archive

import (
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
)

// Comparator orders two opaque source positions: negative if a<b, zero if a==b,
// positive if a>b.
type Comparator func(a, b string) int

type config struct {
	reader       *snapshotter.Reader
	schema       string
	table        string
	keyFields    []string
	follow       bool
	pollInterval time.Duration
	statePath    string
	cmp          Comparator
	ordering     laredo.OrderingGuarantee
}

// Option configures an archive Source.
type Option func(*config)

// WithReader sets the snapshotter reader the source replays from. Required; the
// caller builds it (e.g. via config.BuildArchiveReader / snapshotter.NewReader)
// so this package never depends on destination or format wiring.
func WithReader(r *snapshotter.Reader) Option { return func(c *config) { c.reader = r } }

// Table sets the schema and table this source serves. One archive Source serves
// one table, because a snapshotter manifest is per-table; configure several
// sources (one per table) to replay several tables.
func Table(schema, table string) Option {
	return func(c *config) { c.schema = schema; c.table = table }
}

// KeyFields sets the primary-key column names the archive was written with. They
// key snapshot rows so they fold correctly against the diff stream, and mark the
// primary-key columns when the schema is inferred. Defaults to ["id"], matching
// the snapshotter's default.
func KeyFields(fields ...string) Option { return func(c *config) { c.keyFields = fields } }

// Follow makes Stream keep watching the archive for newly appended diffs — and
// for wholesale replacement — instead of ending when it reaches the head.
// Default false (one-shot: Stream returns when it reaches the head).
func Follow(follow bool) Option { return func(c *config) { c.follow = follow } }

// PollInterval sets how often Stream re-reads the manifest while following
// (default 5s). Ignored when Follow is false.
func PollInterval(d time.Duration) Option { return func(c *config) { c.pollInterval = d } }

// StatePath enables resume across restarts: the last ACKed position is persisted
// to this file, so a restart continues from it instead of re-baselining. Empty
// (default) disables resume — SupportsResume reports false and each start
// re-baselines, the safe default for non-durable in-memory targets.
func StatePath(path string) Option { return func(c *config) { c.statePath = path } }

// WithPositionComparator overrides how positions are ordered. The default is
// PostgreSQL WAL-LSN order (the dominant upstream); override it when the archive
// was written by a non-PostgreSQL source.
func WithPositionComparator(cmp Comparator) Option { return func(c *config) { c.cmp = cmp } }

// WithOrderingGuarantee sets the ordering guarantee reported to the engine.
// Defaults to laredo.TotalOrder (a PostgreSQL-backed archive is totally ordered).
func WithOrderingGuarantee(o laredo.OrderingGuarantee) Option {
	return func(c *config) { c.ordering = o }
}
