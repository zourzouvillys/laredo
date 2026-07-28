// Package sourcesub adapts a laredo.SyncSource to a snapshotter.Subscription, so
// the snapshotter Writer can materialize any source — not just a fan-out — into a
// continuously updated archive (base snapshot + diffs). It is the continuous
// counterpart to snapshotter.WriteBaseSnapshot's one-shot export; see EDR-0006.
//
// The adapter maintains the source's current state in memory (baseline plus
// applied changes, keyed by primary key) so the Writer can re-snapshot on demand,
// and it implements snapshotter.SchemaProvider so the manifest records the source
// schema. If the source signals a re-baseline (ErrReBaselineRequired, e.g. a
// PostgreSQL reconnect), the adapter re-runs the baseline and continues.
package sourcesub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
)

// Adapter presents a laredo.SyncSource as a snapshotter.Subscription.
type Adapter struct {
	src       laredo.SyncSource
	table     laredo.TableIdentifier
	keyFields []string

	mu       sync.Mutex
	state    map[string]laredo.Row
	columns  []laredo.ColumnDefinition
	position string
	onChange func(old, new laredo.Row)

	readyOnce sync.Once
	ready     chan struct{}

	started  bool
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

var (
	_ snapshotter.Subscription   = (*Adapter)(nil)
	_ snapshotter.SchemaProvider = (*Adapter)(nil)
)

// New adapts src (serving table) to a Subscription. keyFields are the primary-key
// columns used to key the in-memory state; empty defaults to ["id"], matching the
// snapshotter.
func New(src laredo.SyncSource, table laredo.TableIdentifier, keyFields []string) *Adapter {
	if len(keyFields) == 0 {
		keyFields = []string{"id"}
	}
	return &Adapter{
		src:       src,
		table:     table,
		keyFields: keyFields,
		state:     make(map[string]laredo.Row),
		ready:     make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start initializes the source, captures the baseline into the in-memory state,
// and begins streaming changes in the background. It returns once the baseline is
// loaded (AwaitReady then succeeds immediately).
func (a *Adapter) Start(ctx context.Context) error {
	schemas, err := a.src.Init(ctx, laredo.SourceConfig{Tables: []laredo.TableIdentifier{a.table}})
	if err != nil {
		return fmt.Errorf("sourcesub: init: %w", err)
	}
	a.mu.Lock()
	a.columns = schemas[a.table]
	a.mu.Unlock()

	pos, err := a.baseline(ctx)
	if err != nil {
		return fmt.Errorf("sourcesub: baseline: %w", err)
	}
	a.readyOnce.Do(func() { close(a.ready) })

	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.mu.Lock()
	a.started = true
	a.cancel = cancel
	a.mu.Unlock()
	go a.stream(streamCtx, pos)
	return nil
}

// baseline replaces the in-memory state with a fresh snapshot from the source and
// records the position it reflects.
func (a *Adapter) baseline(ctx context.Context) (laredo.Position, error) {
	next := make(map[string]laredo.Row)
	pos, err := a.src.Baseline(ctx, []laredo.TableIdentifier{a.table}, func(_ laredo.TableIdentifier, r laredo.Row) {
		next[snapshotter.RowKey(r, a.keyFields)] = r
	})
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.state = next
	a.position = a.src.PositionToString(pos)
	a.mu.Unlock()
	return pos, nil
}

// stream forwards source changes into the state and to the registered callback,
// re-baselining when the source asks for it, until the context is cancelled.
func (a *Adapter) stream(ctx context.Context, from laredo.Position) {
	defer close(a.done)
	handler := laredo.ChangeHandlerFunc(func(ev laredo.ChangeEvent) error {
		a.apply(ev)
		return nil
	})
	for {
		err := a.src.Stream(ctx, from, handler)
		if !errors.Is(err, laredo.ErrReBaselineRequired) {
			return // context cancelled, clean end, or a terminal error
		}
		newPos, berr := a.baseline(ctx)
		if berr != nil {
			return
		}
		from = newPos
	}
}

// apply folds a change into the in-memory state and forwards it to the OnChange
// callback (old=nil for insert, new=nil for delete, both nil for truncate).
func (a *Adapter) apply(ev laredo.ChangeEvent) {
	a.mu.Lock()
	switch ev.Action {
	case laredo.ActionInsert, laredo.ActionUpdate:
		a.state[snapshotter.RowKey(ev.NewValues, a.keyFields)] = ev.NewValues
	case laredo.ActionDelete:
		delete(a.state, snapshotter.RowKey(ev.OldValues, a.keyFields))
	case laredo.ActionTruncate:
		a.state = make(map[string]laredo.Row)
	}
	a.position = a.src.PositionToString(ev.Position)
	fn := a.onChange
	a.mu.Unlock()

	if fn == nil {
		return
	}
	switch ev.Action {
	case laredo.ActionInsert:
		fn(nil, ev.NewValues)
	case laredo.ActionUpdate:
		fn(ev.OldValues, ev.NewValues)
	case laredo.ActionDelete:
		fn(ev.OldValues, nil)
	case laredo.ActionTruncate:
		fn(nil, nil)
	}
}

// AwaitReady blocks until the baseline is loaded or the timeout elapses.
func (a *Adapter) AwaitReady(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-a.ready:
		return true
	case <-timer.C:
		return false
	}
}

// Snapshot returns the current full state and the position it reflects.
func (a *Adapter) Snapshot() (rows []laredo.Row, position string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rows = make([]laredo.Row, 0, len(a.state))
	for _, r := range a.state {
		rows = append(rows, r)
	}
	return rows, a.position
}

// CurrentPosition returns the position of the most recent applied change.
func (a *Adapter) CurrentPosition() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.position
}

// Count returns the current number of rows.
func (a *Adapter) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.state)
}

// OnChange registers the per-change callback.
func (a *Adapter) OnChange(fn func(old, new laredo.Row)) {
	a.mu.Lock()
	a.onChange = fn
	a.mu.Unlock()
}

// Columns reports the source schema, satisfying snapshotter.SchemaProvider so the
// Writer records it in the manifest.
func (a *Adapter) Columns() []laredo.ColumnDefinition {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.columns
}

// Stop cancels streaming, waits for the background goroutine, and closes the
// source. It is safe to call before Start and idempotent — the Writer defers it,
// so a caller may also call it without double-closing the source.
func (a *Adapter) Stop() {
	a.mu.Lock()
	started, cancel := a.started, a.cancel
	a.mu.Unlock()
	if !started {
		return
	}
	a.stopOnce.Do(func() {
		cancel()
		<-a.done
		_ = a.src.Close(context.Background())
	})
}
