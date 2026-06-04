package snapshotter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
)

// Config configures a Writer.
type Config struct {
	// Table is the "schema.table" identifier recorded in the manifest/events.
	Table string
	// KeyPrefix is the object-key prefix for artifacts and the manifest within
	// each destination (e.g. "config_document/"). The manifest lives at
	// "<KeyPrefix>manifest.json".
	KeyPrefix string

	Policy Policy

	// SnapshotFormats and DiffFormats list the encodings to emit. The first of
	// each is the "primary" format, used to measure size for the diff-size
	// triggers. At least one of each is required.
	SnapshotFormats []Format
	DiffFormats     []Format

	// Destinations receive every artifact and the manifest. At least one.
	Destinations []Destination
	// Sinks receive advisory artifact events. May be empty.
	Sinks []EventSink

	// KeyFields are the primary-key field names used to key the diff buffer.
	// Defaults to ["id"].
	KeyFields []string

	// ReadyTimeout bounds the wait for the initial snapshot. Default 60s.
	ReadyTimeout time.Duration
	// KeepEpochs retains this many base+diff generations in the manifest before
	// pruning. Zero disables pruning.
	KeepEpochs int

	// Now is an injectable clock for tests. Defaults to time.Now.
	Now func() time.Time
	// Logger receives operational logs. Defaults to slog.Default().
	Logger *slog.Logger
}

// Writer materializes one table to base snapshots + diffs.
type Writer struct {
	cfg Config
	sub Subscription
	log *slog.Logger
	now func() time.Time

	forceCh chan chan error

	mu                sync.Mutex
	buffer            map[string]Change
	churn             int64
	epoch             int64
	fromPos           string
	lastSnapshotAt    time.Time
	lastSnapshotBytes int64
	headPos           string
	manifest          *Manifest
	revs              map[string]string // destination name → last manifest rev
}

// New creates a Writer over the given subscription and config.
func New(sub Subscription, cfg Config) (*Writer, error) {
	if cfg.Table == "" {
		return nil, errors.New("snapshotter: Table is required")
	}
	if len(cfg.Destinations) == 0 {
		return nil, errors.New("snapshotter: at least one destination is required")
	}
	if len(cfg.SnapshotFormats) == 0 || len(cfg.DiffFormats) == 0 {
		return nil, errors.New("snapshotter: at least one snapshot and one diff format are required")
	}
	if cfg.Policy.DiffInterval <= 0 {
		return nil, errors.New("snapshotter: Policy.DiffInterval must be > 0")
	}
	if len(cfg.KeyFields) == 0 {
		cfg.KeyFields = []string{"id"}
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Writer{
		cfg:      cfg,
		sub:      sub,
		log:      cfg.Logger.With("component", "snapshotter", "table", cfg.Table),
		now:      cfg.Now,
		forceCh:  make(chan chan error),
		buffer:   make(map[string]Change),
		manifest: &Manifest{ManifestVersion: ManifestVersion, Table: cfg.Table},
		revs:     make(map[string]string),
	}, nil
}

// Run subscribes, writes the initial base snapshot, then flushes diffs (re-basing
// on threshold) until ctx is cancelled, flushing a final diff on shutdown.
func (w *Writer) Run(ctx context.Context) error {
	if err := w.sub.Start(ctx); err != nil {
		return fmt.Errorf("snapshotter: start subscription: %w", err)
	}
	defer w.sub.Stop()

	// Register the change handler BEFORE the base snapshot so no change is
	// missed; changes already in the snapshot re-apply idempotently.
	w.sub.OnChange(w.onChange)

	if !w.sub.AwaitReady(w.cfg.ReadyTimeout) {
		return errors.New("snapshotter: subscription not ready before timeout")
	}

	if err := w.loadManifest(ctx); err != nil {
		return err
	}
	if err := w.snapshot(ctx); err != nil {
		return fmt.Errorf("snapshotter: initial snapshot: %w", err)
	}

	ticker := time.NewTicker(w.cfg.Policy.DiffInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Best-effort final flush so buffered changes are durable.
			flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := w.flush(flushCtx); err != nil {
				w.log.Warn("final flush failed", "error", err)
			}
			cancel()
			return ctx.Err()
		case reply := <-w.forceCh:
			reply <- w.snapshot(ctx)
		case <-ticker.C:
			if err := w.flush(ctx); err != nil {
				w.log.Warn("flush failed", "error", err)
			}
		}
	}
}

// Snapshot forces an immediate base snapshot and waits for it to complete.
func (w *Writer) Snapshot(ctx context.Context) error {
	reply := make(chan error, 1)
	select {
	case w.forceCh <- reply:
		return <-reply
	case <-ctx.Done():
		return ctx.Err()
	}
}

// onChange buffers a single change, collapsing repeated edits to the same key.
func (w *Writer) onChange(old, new laredo.Row) {
	row := new
	if row == nil {
		row = old
	}
	key := w.keyOf(row)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.churn++
	switch {
	case new != nil && old == nil:
		w.buffer[key] = Change{Action: laredo.ActionInsert, Key: key, New: new}
	case new != nil && old != nil:
		w.buffer[key] = Change{Action: laredo.ActionUpdate, Key: key, Old: old, New: new}
	case new == nil && old != nil:
		w.buffer[key] = Change{Action: laredo.ActionDelete, Key: key, Old: old}
	default:
		// Both nil = truncate.
		w.buffer = map[string]Change{key: {Action: laredo.ActionTruncate, Key: key}}
	}
}

// snapshot writes a fresh base snapshot and re-bases the diff chain.
//
// The buffer is discarded *before* taking the subscription snapshot, so the
// snapshot reflects every discarded change (no loss); changes that race in
// afterward land in the fresh buffer and may duplicate snapshot content
// (idempotent on replay).
func (w *Writer) snapshot(ctx context.Context) error {
	w.mu.Lock()
	w.buffer = make(map[string]Change)
	w.churn = 0
	epoch := w.epoch + 1
	w.mu.Unlock()

	rows, pos := w.sub.Snapshot()

	art := Artifact{
		Kind:       KindSnapshot,
		Epoch:      epoch,
		ToPosition: pos,
		CreatedAt:  w.now().UTC(),
		RowCount:   int64(len(rows)),
		Formats:    map[string]FormatRef{},
	}
	var primarySize int64
	for i, f := range w.cfg.SnapshotFormats {
		var buf bytes.Buffer
		if err := f.WriteSnapshot(&buf, rows); err != nil {
			return fmt.Errorf("encode snapshot (%s): %w", f.FormatID(), err)
		}
		ref, err := w.putArtifact(ctx, art, f.FormatID(), f.Extension(), buf.Bytes())
		if err != nil {
			return err
		}
		art.Formats[f.FormatID()] = ref
		if i == 0 {
			primarySize = ref.SizeBytes
		}
	}

	if err := w.commit(ctx, art); err != nil {
		return err
	}

	w.mu.Lock()
	w.epoch = epoch
	w.fromPos = pos
	w.headPos = pos
	w.lastSnapshotAt = w.now()
	w.lastSnapshotBytes = primarySize
	w.mu.Unlock()

	w.log.Info("wrote snapshot", "epoch", epoch, "rows", art.RowCount, "position", pos, "bytes", primarySize)
	w.emit(ctx, art)
	return nil
}

// flush serializes the buffered changes into a diff — or re-bases if a threshold
// fires. On a write failure the drained changes are merged back so they retry.
func (w *Writer) flush(ctx context.Context) error {
	w.mu.Lock()
	if len(w.buffer) == 0 {
		w.mu.Unlock()
		return nil
	}
	changes := make([]Change, 0, len(w.buffer))
	for _, c := range w.buffer {
		changes = append(changes, c)
	}
	drained := w.buffer
	w.buffer = make(map[string]Change)
	churn := w.churn
	from := w.fromPos
	lastSnapBytes := w.lastSnapshotBytes
	since := w.now().Sub(w.lastSnapshotAt)
	epoch := w.epoch
	w.mu.Unlock()

	to := w.sub.CurrentPosition()

	// Encode the primary diff format once, both to measure size and to reuse.
	primary := w.cfg.DiffFormats[0]
	var primaryBuf bytes.Buffer
	if err := primary.WriteDiff(&primaryBuf, changes); err != nil {
		w.remerge(drained)
		return fmt.Errorf("encode diff (%s): %w", primary.FormatID(), err)
	}

	if snap, why := w.cfg.Policy.shouldSnapshot(triggerState{
		sinceSnapshot:     since,
		diffBytes:         int64(primaryBuf.Len()),
		churnRecords:      churn,
		datasetSize:       int64(w.sub.Count()),
		lastSnapshotBytes: lastSnapBytes,
	}); snap {
		w.log.Info("re-basing instead of diff", "trigger", why)
		// The drained changes are captured by the upcoming snapshot.
		return w.snapshot(ctx)
	}

	fromCopy := from
	art := Artifact{
		Kind:         KindDiff,
		Epoch:        epoch,
		FromPosition: &fromCopy,
		ToPosition:   to,
		CreatedAt:    w.now().UTC(),
		ChangeCount:  int64(len(changes)),
		Formats:      map[string]FormatRef{},
	}
	for i, f := range w.cfg.DiffFormats {
		data := primaryBuf.Bytes()
		if i != 0 {
			var buf bytes.Buffer
			if err := f.WriteDiff(&buf, changes); err != nil {
				w.remerge(drained)
				return fmt.Errorf("encode diff (%s): %w", f.FormatID(), err)
			}
			data = buf.Bytes()
		}
		ref, err := w.putArtifact(ctx, art, f.FormatID(), f.Extension(), data)
		if err != nil {
			w.remerge(drained)
			return err
		}
		art.Formats[f.FormatID()] = ref
	}

	if err := w.commit(ctx, art); err != nil {
		w.remerge(drained)
		return err
	}

	w.mu.Lock()
	w.fromPos = to
	w.headPos = to
	w.mu.Unlock()

	w.log.Info("wrote diff", "epoch", epoch, "changes", art.ChangeCount, "from", from, "to", to)
	w.emit(ctx, art)
	return nil
}

// remerge restores drained changes after a failed flush, letting newer edits win.
func (w *Writer) remerge(drained map[string]Change) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, c := range drained {
		if _, ok := w.buffer[k]; !ok {
			w.buffer[k] = c
		}
	}
}

// Status is a point-in-time view of the writer for the operational API.
type Status struct {
	Table          string    `json:"table"`
	Epoch          int64     `json:"epoch"`
	HeadPosition   string    `json:"head_position"`
	BufferDepth    int       `json:"buffer_depth"`
	ChurnRecords   int64     `json:"churn_records"`
	LastSnapshotAt time.Time `json:"last_snapshot_at"`
}

// Status returns the current writer status.
func (w *Writer) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Status{
		Table:          w.cfg.Table,
		Epoch:          w.epoch,
		HeadPosition:   w.headPos,
		BufferDepth:    len(w.buffer),
		ChurnRecords:   w.churn,
		LastSnapshotAt: w.lastSnapshotAt,
	}
}

func (w *Writer) keyOf(row laredo.Row) string {
	if len(w.cfg.KeyFields) == 1 {
		return fmt.Sprintf("%v", row[w.cfg.KeyFields[0]])
	}
	parts := make([]any, len(w.cfg.KeyFields))
	for i, f := range w.cfg.KeyFields {
		parts[i] = row[f]
	}
	return fmt.Sprintf("%v", parts)
}
