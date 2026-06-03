package snapshotter

import (
	"context"
	"time"

	"github.com/zourzouvillys/laredo"
)

// Subscription is the live view of a table the writer materializes. It is
// satisfied by an adapter over client/fanout (see snapshotter/fanoutsub) and by
// fakes in tests, so the Writer never depends on a concrete client.
//
// Lock discipline: the Writer never calls a Subscription method while holding
// its own lock, and a Subscription must not call back into the Writer from
// inside an OnChange callback while the Writer holds a lock — OnChange callbacks
// are expected to be cheap and non-reentrant.
type Subscription interface {
	// Start connects and begins receiving data (non-blocking).
	Start(ctx context.Context) error
	// AwaitReady blocks until the initial state is loaded or the timeout passes.
	AwaitReady(timeout time.Duration) bool
	// Snapshot returns the full current state and the source position it
	// represents.
	Snapshot() (rows []laredo.Row, position string)
	// CurrentPosition returns the source position of the most recent applied
	// change (the high-water mark).
	CurrentPosition() string
	// Count returns the current number of rows.
	Count() int
	// OnChange registers a callback invoked for each change (old=nil for insert,
	// new=nil for delete). Registered before the base snapshot so no change is
	// missed; changes already reflected in the snapshot re-apply idempotently.
	OnChange(fn func(old, new laredo.Row))
	// Stop disconnects.
	Stop()
}
