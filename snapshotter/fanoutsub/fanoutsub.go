// Package fanoutsub adapts a client/fanout.Client to the snapshotter.Subscription
// interface so the Writer can materialize a fan-out table.
package fanoutsub

import (
	"context"
	"time"

	"github.com/zourzouvillys/laredo"
	fanout "github.com/zourzouvillys/laredo/client/fanout"
	"github.com/zourzouvillys/laredo/snapshotter"
)

// Subscription wraps a fan-out client.
type Subscription struct {
	c *fanout.Client
}

// New builds a fan-out client from options and wraps it.
func New(opts ...fanout.Option) *Subscription {
	return &Subscription{c: fanout.New(opts...)}
}

// Wrap adapts an existing fan-out client.
func Wrap(c *fanout.Client) *Subscription { return &Subscription{c: c} }

var _ snapshotter.Subscription = (*Subscription)(nil)

// Start implements snapshotter.Subscription.
func (s *Subscription) Start(ctx context.Context) error { return s.c.Start(ctx) }

// AwaitReady implements snapshotter.Subscription.
func (s *Subscription) AwaitReady(timeout time.Duration) bool { return s.c.AwaitReady(timeout) }

// Snapshot returns the full current state and a source position that is at or
// before the returned state. The position is read first so it can only lag the
// rows, never lead them — a conservative watermark that guarantees the diff
// chain after it has no gap (changes already in the snapshot merely re-apply).
func (s *Subscription) Snapshot() ([]laredo.Row, string) {
	pos := s.c.LastSourcePosition()
	rows := s.c.All()
	return rows, pos
}

// CurrentPosition implements snapshotter.Subscription.
func (s *Subscription) CurrentPosition() string { return s.c.LastSourcePosition() }

// Count implements snapshotter.Subscription.
func (s *Subscription) Count() int { return s.c.Count() }

// OnChange implements snapshotter.Subscription.
func (s *Subscription) OnChange(fn func(old, new laredo.Row)) { s.c.Listen(fn) }

// Stop implements snapshotter.Subscription.
func (s *Subscription) Stop() { s.c.Stop() }
