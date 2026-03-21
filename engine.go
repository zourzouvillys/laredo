package laredo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo/internal/engine"
)

// Engine orchestrates pipelines binding sources to targets.
type Engine interface {
	// Start begins baseline/restore and streaming in background goroutines.
	Start(ctx context.Context) error

	// Stop performs a graceful shutdown (optionally taking a snapshot).
	Stop(ctx context.Context) error

	// AwaitReady blocks until all pipelines are ready or the timeout expires.
	AwaitReady(timeout time.Duration) bool

	// IsReady reports whether all pipelines are ready.
	IsReady() bool

	// IsSourceReady reports whether all pipelines for a source are ready.
	IsSourceReady(sourceID string) bool

	// OnReady registers a callback that is invoked when all pipelines are ready.
	// If already ready, the callback is invoked immediately.
	OnReady(callback func())

	// Reload triggers a re-baseline for a specific table on a source.
	Reload(ctx context.Context, sourceID string, table TableIdentifier) error

	// Pause pauses the given source.
	Pause(ctx context.Context, sourceID string) error

	// Resume resumes the given source.
	Resume(ctx context.Context, sourceID string) error

	// CreateSnapshot triggers a snapshot.
	CreateSnapshot(ctx context.Context, userMeta map[string]Value) error

	// Targets returns all targets bound to the given source and table.
	Targets(sourceID string, table TableIdentifier) []SyncTarget
}

// Option configures the engine.
type Option func(*engineConfig)

type engineConfig struct {
	sources   map[string]SyncSource
	pipelines []pipelineConfig
	observer  EngineObserver

	snapshotStore      SnapshotStore
	snapshotSerializer SnapshotSerializer
	snapshotSchedule   time.Duration
	snapshotOnShutdown bool
	shutdownTimeout    time.Duration
	deadLetterStore    DeadLetterStore
}

type pipelineConfig struct {
	sourceID     string
	table        TableIdentifier
	target       SyncTarget
	filters      []PipelineFilter
	transforms   []PipelineTransform
	bufferSize   int
	bufferPolicy BufferPolicy
	errorPolicy  ErrorPolicyKind
	maxRetries   int
}

// WithSource registers a named source.
func WithSource(id string, source SyncSource) Option {
	return func(c *engineConfig) {
		if c.sources == nil {
			c.sources = make(map[string]SyncSource)
		}
		c.sources[id] = source
	}
}

// PipelineOption configures a pipeline.
type PipelineOption func(*pipelineConfig)

// WithPipeline adds a pipeline binding a source table to a target.
func WithPipeline(sourceID string, table TableIdentifier, target SyncTarget, opts ...PipelineOption) Option {
	return func(c *engineConfig) {
		pc := pipelineConfig{
			sourceID:     sourceID,
			table:        table,
			target:       target,
			bufferSize:   10000,
			bufferPolicy: BufferBlock,
			errorPolicy:  ErrorIsolate,
			maxRetries:   5,
		}
		for _, opt := range opts {
			opt(&pc)
		}
		c.pipelines = append(c.pipelines, pc)
	}
}

// PipelineFilterOpt adds a filter to a pipeline.
func PipelineFilterOpt(f PipelineFilter) PipelineOption {
	return func(c *pipelineConfig) {
		c.filters = append(c.filters, f)
	}
}

// PipelineTransformOpt adds a transform to a pipeline.
func PipelineTransformOpt(t PipelineTransform) PipelineOption {
	return func(c *pipelineConfig) {
		c.transforms = append(c.transforms, t)
	}
}

// BufferSize sets the pipeline buffer size.
func BufferSize(n int) PipelineOption {
	return func(c *pipelineConfig) {
		c.bufferSize = n
	}
}

// BufferPolicyOpt sets the pipeline buffer policy.
func BufferPolicyOpt(p BufferPolicy) PipelineOption {
	return func(c *pipelineConfig) {
		c.bufferPolicy = p
	}
}

// ErrorPolicyOpt sets the pipeline error policy.
func ErrorPolicyOpt(p ErrorPolicyKind) PipelineOption {
	return func(c *pipelineConfig) {
		c.errorPolicy = p
	}
}

// MaxRetries sets the pipeline max retries.
func MaxRetries(n int) PipelineOption {
	return func(c *pipelineConfig) {
		c.maxRetries = n
	}
}

// WithObserver sets the engine observer.
func WithObserver(o EngineObserver) Option {
	return func(c *engineConfig) {
		c.observer = o
	}
}

// WithSnapshotStore sets the snapshot store.
func WithSnapshotStore(s SnapshotStore) Option {
	return func(c *engineConfig) {
		c.snapshotStore = s
	}
}

// WithSnapshotSerializer sets the snapshot serializer.
func WithSnapshotSerializer(s SnapshotSerializer) Option {
	return func(c *engineConfig) {
		c.snapshotSerializer = s
	}
}

// WithSnapshotSchedule sets the periodic snapshot interval.
func WithSnapshotSchedule(d time.Duration) Option {
	return func(c *engineConfig) {
		c.snapshotSchedule = d
	}
}

// WithSnapshotOnShutdown enables taking a snapshot on shutdown.
func WithSnapshotOnShutdown(enabled bool) Option {
	return func(c *engineConfig) {
		c.snapshotOnShutdown = enabled
	}
}

// WithShutdownTimeout sets a timeout for graceful shutdown. If the timeout
// expires before all goroutines finish, Stop returns context.DeadlineExceeded.
// Zero means no timeout (wait indefinitely).
func WithShutdownTimeout(d time.Duration) Option {
	return func(c *engineConfig) {
		c.shutdownTimeout = d
	}
}

// WithDeadLetterStore sets the dead letter store for failed changes.
// When configured, changes that fail after all retries are written to the
// store before the error policy is applied.
func WithDeadLetterStore(store DeadLetterStore) Option {
	return func(c *engineConfig) {
		c.deadLetterStore = store
	}
}

// NewEngine creates an engine with the given options.
// Returns the engine and any configuration validation errors.
func NewEngine(opts ...Option) (Engine, []error) {
	cfg := &engineConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if errs := validateConfig(cfg); len(errs) > 0 {
		return nil, errs
	}

	observer := cfg.observer
	if observer == nil {
		observer = NullObserver{}
	}

	pipelines := make([]resolvedPipeline, len(cfg.pipelines))
	pipelineIDs := make([]string, len(cfg.pipelines))
	for i, pc := range cfg.pipelines {
		id := generatePipelineID(pc.sourceID, pc.table, pc.target)
		pipelines[i] = resolvedPipeline{
			id:           id,
			sourceID:     pc.sourceID,
			table:        pc.table,
			target:       pc.target,
			filters:      pc.filters,
			transforms:   pc.transforms,
			bufferSize:   pc.bufferSize,
			bufferPolicy: pc.bufferPolicy,
			errorPolicy:  pc.errorPolicy,
			maxRetries:   pc.maxRetries,
			state:        PipelineInitializing,
		}
		pipelineIDs[i] = id
	}

	ackTracker := engine.NewAckTracker()
	for i, pc := range cfg.pipelines {
		src := cfg.sources[pc.sourceID]
		if src != nil {
			ackTracker.RegisterPipeline(pipelines[i].id, pc.sourceID, src.ComparePositions)
		}
	}

	return &coreEngine{
		sources:            cfg.sources,
		pipelines:          pipelines,
		observer:           observer,
		readiness:          engine.NewReadinessTracker(pipelineIDs),
		ackTracker:         ackTracker,
		snapshotStore:      cfg.snapshotStore,
		snapshotSerializer: cfg.snapshotSerializer,
		snapshotSchedule:   cfg.snapshotSchedule,
		snapshotOnShutdown: cfg.snapshotOnShutdown,
		shutdownTimeout:    cfg.shutdownTimeout,
		deadLetterStore:    cfg.deadLetterStore,
	}, nil
}

// validateConfig checks the engine configuration for errors.
func validateConfig(cfg *engineConfig) []error {
	var errs []error

	if len(cfg.sources) == 0 {
		errs = append(errs, fmt.Errorf("at least one source is required"))
	}

	for id, src := range cfg.sources {
		if id == "" {
			errs = append(errs, fmt.Errorf("source ID must not be empty"))
		}
		if src == nil {
			errs = append(errs, fmt.Errorf("source %q must not be nil", id))
		}
	}

	if len(cfg.pipelines) == 0 {
		errs = append(errs, fmt.Errorf("at least one pipeline is required"))
	}

	seen := make(map[string]bool)
	for i, pc := range cfg.pipelines {
		prefix := fmt.Sprintf("pipeline[%d]", i)

		if pc.sourceID == "" {
			errs = append(errs, fmt.Errorf("%s: source ID must not be empty", prefix))
		} else if _, ok := cfg.sources[pc.sourceID]; !ok {
			errs = append(errs, fmt.Errorf("%s: references unknown source %q", prefix, pc.sourceID))
		}

		if pc.table.Table == "" {
			errs = append(errs, fmt.Errorf("%s: table name must not be empty", prefix))
		}

		if pc.target == nil {
			errs = append(errs, fmt.Errorf("%s: target must not be nil", prefix))
		}

		if pc.bufferSize <= 0 {
			errs = append(errs, fmt.Errorf("%s: buffer size must be positive, got %d", prefix, pc.bufferSize))
		}

		if pc.maxRetries < 0 {
			errs = append(errs, fmt.Errorf("%s: max retries must be non-negative, got %d", prefix, pc.maxRetries))
		}

		if pc.target != nil {
			id := generatePipelineID(pc.sourceID, pc.table, pc.target)
			if seen[id] {
				errs = append(errs, fmt.Errorf("%s: duplicate pipeline ID %q", prefix, id))
			}
			seen[id] = true
		}
	}

	if cfg.snapshotSchedule < 0 {
		errs = append(errs, fmt.Errorf("snapshot schedule must not be negative"))
	}

	if cfg.snapshotStore != nil && cfg.snapshotSerializer == nil {
		errs = append(errs, fmt.Errorf("snapshot serializer is required when snapshot store is configured"))
	}

	return errs
}

// generatePipelineID creates a pipeline ID from its components.
// Format: "{sourceID}:{schema}.{table}:{targetType}"
func generatePipelineID(sourceID string, table TableIdentifier, target SyncTarget) string {
	return fmt.Sprintf("%s:%s:%s", sourceID, table.String(), targetTypeName(target))
}

// targetTypeName returns a short type name for a target, stripping the pointer prefix.
func targetTypeName(target SyncTarget) string {
	return strings.TrimPrefix(reflect.TypeOf(target).String(), "*")
}

// resolvedPipeline holds the fully resolved configuration and runtime state for a pipeline.
type resolvedPipeline struct {
	id           string
	sourceID     string
	table        TableIdentifier
	target       SyncTarget
	filters      []PipelineFilter
	transforms   []PipelineTransform
	bufferSize   int
	bufferPolicy BufferPolicy
	errorPolicy  ErrorPolicyKind
	maxRetries   int
	state        PipelineState
}

var (
	errNotStarted     = errors.New("engine not started")
	errAlreadyStarted = errors.New("engine already started")
	errStopped        = errors.New("engine stopped")
)

// coreEngine is the internal Engine implementation.
type coreEngine struct {
	sources    map[string]SyncSource
	pipelines  []resolvedPipeline
	observer   EngineObserver
	readiness  *engine.ReadinessTracker
	ackTracker *engine.AckTracker

	snapshotStore      SnapshotStore
	snapshotSerializer SnapshotSerializer
	snapshotSchedule   time.Duration
	snapshotOnShutdown bool
	shutdownTimeout    time.Duration
	deadLetterStore    DeadLetterStore

	mu      sync.RWMutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

var _ Engine = (*coreEngine)(nil)

//nolint:revive // Engine interface implementation; docs are on the interface.
func (e *coreEngine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return errStopped
	}
	if e.started {
		return errAlreadyStarted
	}
	e.started = true

	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel

	// Group pipelines by source for initialization.
	sourceGroups := e.groupPipelinesBySource()

	for sourceID, pipelineIdxs := range sourceGroups {
		e.wg.Add(1)
		go e.runSource(runCtx, sourceID, pipelineIdxs)
	}

	return nil
}

// groupPipelinesBySource returns a map from source ID to pipeline indices.
func (e *coreEngine) groupPipelinesBySource() map[string][]int {
	groups := make(map[string][]int)
	for i := range e.pipelines {
		sid := e.pipelines[i].sourceID
		groups[sid] = append(groups[sid], i)
	}
	return groups
}

// runSource runs the lifecycle for a single source: init → baseline → stream.
func (e *coreEngine) runSource(ctx context.Context, sourceID string, pipelineIdxs []int) {
	defer e.wg.Done()

	source := e.sources[sourceID]

	// Collect unique tables for this source.
	tables := e.tablesForPipelines(pipelineIdxs)

	// Init the source.
	schemas, err := source.Init(ctx, SourceConfig{Tables: tables})
	if err != nil {
		e.transitionPipelines(pipelineIdxs, PipelineError)
		e.observer.OnSourceDisconnected(sourceID, fmt.Sprintf("init failed: %v", err))
		return
	}

	e.observer.OnSourceConnected(sourceID, reflect.TypeOf(source).String())

	// Init each target with the column schema.
	for _, idx := range pipelineIdxs {
		p := &e.pipelines[idx]
		cols := schemas[p.table]
		if err := p.target.OnInit(ctx, p.table, cols); err != nil {
			e.transitionPipeline(idx, PipelineError)
			continue
		}
	}

	// Try resume path: if the source supports resume and has a last ACKed
	// position, skip baseline and go directly to streaming.
	var position Position
	if source.SupportsResume() {
		lastPos, err := source.LastAckedPosition(ctx)
		if err == nil && lastPos != nil {
			position = lastPos

			// Skip baseline — transition directly to STREAMING.
			for _, idx := range pipelineIdxs {
				p := &e.pipelines[idx]
				if p.state == PipelineInitializing {
					e.transitionPipeline(idx, PipelineStreaming)
					e.readiness.SetReady(p.id)
				}
			}

			e.streamFromPosition(ctx, sourceID, source, pipelineIdxs, position)
			return
		}
	}

	// Try snapshot restore: load latest snapshot, restore targets, resume from
	// the snapshot's source position.
	if e.snapshotStore != nil {
		if pos := e.trySnapshotRestore(ctx, sourceID, source, pipelineIdxs); pos != nil {
			e.streamFromPosition(ctx, sourceID, source, pipelineIdxs, pos)
			return
		}
	}

	// Full baseline path.
	position = e.runBaseline(ctx, sourceID, source, tables, pipelineIdxs)
	if position == nil {
		return // baseline failed or context cancelled
	}

	e.streamFromPosition(ctx, sourceID, source, pipelineIdxs, position)
}

// runBaseline performs the full baseline flow: transition to BASELINING, read all
// rows from the source, dispatch to targets, complete. Returns the baseline position
// or nil if the baseline failed.
// trySnapshotRestore attempts to restore targets from the latest snapshot.
// Returns the source position to resume from, or nil if restore failed or
// no usable snapshot exists (caller should fall through to full baseline).
func (e *coreEngine) trySnapshotRestore(ctx context.Context, sourceID string, _ SyncSource, pipelineIdxs []int) Position {
	// List snapshots (newest first).
	snapshots, err := e.snapshotStore.List(ctx, nil)
	if err != nil || len(snapshots) == 0 {
		return nil
	}

	// Load the latest snapshot.
	snapshotID := snapshots[0].SnapshotID
	e.observer.OnSnapshotRestoreStarted(snapshotID)
	restoreStart := time.Now()

	metadata, entries, err := e.snapshotStore.Load(ctx, snapshotID)
	if err != nil {
		e.observer.OnSnapshotFailed(snapshotID, ErrorInfo{Err: err, Message: fmt.Sprintf("load snapshot: %v", err)})
		return nil
	}

	// Check for a source position to resume from.
	var resumePos Position
	if metadata.SourcePositions != nil {
		resumePos = metadata.SourcePositions[sourceID]
	}
	if resumePos == nil {
		// No position for this source — snapshot unusable, fall through to baseline.
		e.observer.OnSnapshotFailed(snapshotID, ErrorInfo{
			Message: fmt.Sprintf("snapshot has no position for source %s", sourceID),
		})
		return nil
	}

	// Restore each target from snapshot data.
	for _, idx := range pipelineIdxs {
		p := &e.pipelines[idx]
		if p.state != PipelineInitializing {
			continue
		}

		tableEntries := entries[p.table]
		tableInfo := findSnapshotTableInfo(metadata.Tables, p.table)

		if err := p.target.RestoreSnapshot(ctx, tableInfo, tableEntries); err != nil {
			// Snapshot unusable for this target — fall through to baseline.
			e.observer.OnSnapshotFailed(snapshotID, ErrorInfo{
				Err:     err,
				Message: fmt.Sprintf("restore target %s: %v", p.id, err),
			})
			return nil
		}
	}

	// All targets restored — transition to STREAMING.
	for _, idx := range pipelineIdxs {
		p := &e.pipelines[idx]
		if p.state == PipelineInitializing {
			e.transitionPipeline(idx, PipelineStreaming)
			e.readiness.SetReady(p.id)
		}
	}

	e.observer.OnSnapshotRestoreCompleted(snapshotID, time.Since(restoreStart))
	return resumePos
}

// findSnapshotTableInfo finds the TableSnapshotInfo for a table in the metadata,
// or returns a zero value if not found.
func findSnapshotTableInfo(tables []TableSnapshotInfo, table TableIdentifier) TableSnapshotInfo {
	for _, t := range tables {
		if t.Table == table {
			return t
		}
	}
	return TableSnapshotInfo{Table: table}
}

func (e *coreEngine) runBaseline(ctx context.Context, sourceID string, source SyncSource, tables []TableIdentifier, pipelineIdxs []int) Position {
	e.transitionPipelines(pipelineIdxs, PipelineBaselining)

	rowCounts := make(map[TableIdentifier]int64, len(tables))
	baselineStart := time.Now()

	for _, idx := range pipelineIdxs {
		p := &e.pipelines[idx]
		if p.state == PipelineBaselining {
			e.observer.OnBaselineStarted(p.id, p.table)
		}
	}

	position, err := source.Baseline(ctx, tables, func(table TableIdentifier, row Row) {
		rowCounts[table]++
		e.dispatchBaselineRow(ctx, pipelineIdxs, table, row)
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		e.transitionPipelines(pipelineIdxs, PipelineError)
		e.observer.OnSourceDisconnected(sourceID, fmt.Sprintf("baseline failed: %v", err))
		return nil
	}

	baselineDuration := time.Since(baselineStart)

	// Notify baseline complete for each pipeline.
	for _, idx := range pipelineIdxs {
		p := &e.pipelines[idx]
		if p.state != PipelineBaselining {
			continue
		}
		if err := p.target.OnBaselineComplete(ctx, p.table); err != nil {
			e.transitionPipeline(idx, PipelineError)
			continue
		}
		e.observer.OnBaselineCompleted(p.id, p.table, rowCounts[p.table], baselineDuration)
	}

	// Transition to streaming.
	for _, idx := range pipelineIdxs {
		p := &e.pipelines[idx]
		if p.state != PipelineBaselining {
			continue
		}
		e.transitionPipeline(idx, PipelineStreaming)
		e.readiness.SetReady(p.id)
	}

	return position
}

// streamFromPosition starts streaming changes from the given position.
func (e *coreEngine) streamFromPosition(ctx context.Context, sourceID string, source SyncSource, pipelineIdxs []int, position Position) {
	err := source.Stream(ctx, position, ChangeHandlerFunc(func(event ChangeEvent) error {
		return e.dispatchChange(ctx, sourceID, source, pipelineIdxs, event)
	}))
	if err != nil && ctx.Err() == nil {
		e.observer.OnSourceDisconnected(sourceID, fmt.Sprintf("stream error: %v", err))
		e.transitionPipelines(pipelineIdxs, PipelineError)
	}
}

// tablesForPipelines returns the unique tables referenced by the given pipeline indices.
func (e *coreEngine) tablesForPipelines(pipelineIdxs []int) []TableIdentifier {
	seen := make(map[TableIdentifier]bool)
	var tables []TableIdentifier
	for _, idx := range pipelineIdxs {
		t := e.pipelines[idx].table
		if !seen[t] {
			seen[t] = true
			tables = append(tables, t)
		}
	}
	return tables
}

// dispatchBaselineRow delivers a baseline row to all matching pipelines, applying
// filters and transforms.
func (e *coreEngine) dispatchBaselineRow(ctx context.Context, pipelineIdxs []int, table TableIdentifier, row Row) {
	for _, idx := range pipelineIdxs {
		p := &e.pipelines[idx]
		if p.table != table || p.state != PipelineBaselining {
			continue
		}
		r := applyFiltersAndTransforms(p, table, row)
		if r == nil {
			continue
		}
		if err := p.target.OnBaselineRow(ctx, table, r); err != nil {
			e.transitionPipeline(idx, PipelineError)
		}
	}
}

// dispatchChange delivers a change event to all matching pipelines, applying
// filters and transforms. After all pipelines are processed, it advances
// the ACK position if all durable targets have confirmed.
func (e *coreEngine) dispatchChange(ctx context.Context, sourceID string, source SyncSource, pipelineIdxs []int, event ChangeEvent) error {
	for _, idx := range pipelineIdxs {
		p := &e.pipelines[idx]
		if p.table != event.Table || p.state != PipelineStreaming {
			continue
		}

		start := time.Now()
		e.observer.OnChangeReceived(p.id, event.Table, event.Action, event.Position)

		err := e.applyChangeToTarget(ctx, p, event)
		if err != nil {
			// Report the initial error.
			e.observer.OnChangeError(p.id, event.Table, event.Action, ErrorInfo{Err: err, Message: err.Error()})
			// Retry with exponential backoff.
			err = e.retryChange(ctx, p, event, err)
		}

		if err != nil {
			// Write to dead letter store if configured.
			if e.deadLetterStore != nil {
				errInfo := ErrorInfo{Err: err, Message: err.Error()}
				if dlErr := e.deadLetterStore.Write(p.id, event, errInfo); dlErr == nil {
					e.observer.OnDeadLetterWritten(p.id, event, errInfo)
				}
			}
			if e.applyErrorPolicy(ctx, idx, sourceID, pipelineIdxs) {
				return fmt.Errorf("engine stopped due to error policy")
			}
			continue
		}
		e.observer.OnChangeApplied(p.id, event.Table, event.Action, time.Since(start))

		// Confirm the position if the target has durably processed it.
		if p.target.IsDurable() {
			e.ackTracker.Confirm(p.id, event.Position)
		}
	}

	// Try to advance the ACK position for this source.
	if pos, advanced := e.ackTracker.AckPosition(sourceID); advanced {
		if err := source.Ack(ctx, pos); err == nil {
			e.observer.OnAckAdvanced(sourceID, pos)
		}
	}

	return nil
}

// applyChangeToTarget applies a single change event to a pipeline's target.
func (e *coreEngine) applyChangeToTarget(ctx context.Context, p *resolvedPipeline, event ChangeEvent) error {
	switch event.Action {
	case ActionInsert:
		row := applyFiltersAndTransforms(p, event.Table, event.NewValues)
		if row == nil {
			return nil
		}
		return p.target.OnInsert(ctx, event.Table, row)
	case ActionUpdate:
		row := applyFiltersAndTransforms(p, event.Table, event.NewValues)
		if row == nil {
			return nil
		}
		return p.target.OnUpdate(ctx, event.Table, row, event.OldValues)
	case ActionDelete:
		if !applyFilters(p, event.Table, event.OldValues) {
			return nil
		}
		return p.target.OnDelete(ctx, event.Table, event.OldValues)
	case ActionTruncate:
		return p.target.OnTruncate(ctx, event.Table)
	default:
		return nil
	}
}

// retryChange retries a failed change with exponential backoff.
// Returns nil if a retry succeeds, or the last error if all retries fail.
func (e *coreEngine) retryChange(ctx context.Context, p *resolvedPipeline, event ChangeEvent, lastErr error) error {
	backoff := 100 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for attempt := 1; attempt <= p.maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		err := e.applyChangeToTarget(ctx, p, event)
		if err == nil {
			return nil
		}
		lastErr = err

		e.observer.OnChangeError(p.id, event.Table, event.Action, ErrorInfo{
			Err:     err,
			Message: fmt.Sprintf("retry %d/%d: %v", attempt, p.maxRetries, err),
		})

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	return lastErr
}

// applyErrorPolicy applies the pipeline's error policy after retries are exhausted.
// Returns true if the engine should stop (StopAll policy).
func (e *coreEngine) applyErrorPolicy(ctx context.Context, failedIdx int, sourceID string, pipelineIdxs []int) bool {
	p := &e.pipelines[failedIdx]

	switch p.errorPolicy {
	case ErrorIsolate:
		e.transitionPipeline(failedIdx, PipelineError)
		e.ackTracker.Skip(p.id)

	case ErrorStopSource:
		for _, idx := range pipelineIdxs {
			pp := &e.pipelines[idx]
			if pp.sourceID == sourceID && pp.state != PipelineError && pp.state != PipelineStopped {
				e.transitionPipeline(idx, PipelineError)
				e.ackTracker.Skip(pp.id)
			}
		}

	case ErrorStopAll:
		// Transition all pipelines to ERROR and signal engine stop.
		for i := range e.pipelines {
			pp := &e.pipelines[i]
			if pp.state != PipelineError && pp.state != PipelineStopped {
				e.transitionPipeline(i, PipelineError)
				e.ackTracker.Skip(pp.id)
			}
		}
		if e.cancel != nil {
			e.cancel()
		}
		return true
	}

	return false
}

// applyFilters runs only the pipeline's filter chain on a row.
// Returns false if the row is filtered out. Used for DELETE events where
// transforms should not modify the identity.
func applyFilters(p *resolvedPipeline, table TableIdentifier, row Row) bool {
	for _, f := range p.filters {
		if !f.Include(table, row) {
			return false
		}
	}
	return true
}

// applyFiltersAndTransforms runs the pipeline's filter and transform chains on a row.
// Returns nil if the row is filtered out.
func applyFiltersAndTransforms(p *resolvedPipeline, table TableIdentifier, row Row) Row {
	for _, f := range p.filters {
		if !f.Include(table, row) {
			return nil
		}
	}
	r := row
	for _, t := range p.transforms {
		r = t.Transform(table, r)
		if r == nil {
			return nil
		}
	}
	return r
}

// transitionPipeline transitions a single pipeline to a new state and fires the observer.
func (e *coreEngine) transitionPipeline(idx int, newState PipelineState) {
	p := &e.pipelines[idx]
	old := p.state
	if old == newState {
		return
	}
	p.state = newState
	e.observer.OnPipelineStateChanged(p.id, old, newState)
}

// transitionPipelines transitions all pipelines that are not already in ERROR state.
func (e *coreEngine) transitionPipelines(idxs []int, newState PipelineState) {
	for _, idx := range idxs {
		p := &e.pipelines[idx]
		if p.state == PipelineError || p.state == PipelineStopped {
			continue
		}
		e.transitionPipeline(idx, newState)
	}
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Stop(ctx context.Context) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return errStopped
	}
	if !e.started {
		e.mu.Unlock()
		return errNotStarted
	}
	e.stopped = true
	cancel := e.cancel
	e.mu.Unlock()

	// Apply shutdown timeout if configured.
	if e.shutdownTimeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, e.shutdownTimeout)
		defer timeoutCancel()
	}

	// Cancel all source goroutines and wait for them to finish.
	cancel()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Take snapshot on shutdown if configured.
	if e.snapshotOnShutdown && e.snapshotStore != nil {
		if err := e.doSnapshot(ctx, map[string]Value{"trigger": "shutdown"}); err != nil {
			e.observer.OnSnapshotFailed("shutdown", ErrorInfo{Err: err, Message: fmt.Sprintf("shutdown snapshot: %v", err)})
		}
	}

	// ACK final positions for each source.
	for sourceID, source := range e.sources {
		if pos, advanced := e.ackTracker.AckPosition(sourceID); advanced {
			if err := source.Ack(ctx, pos); err == nil {
				e.observer.OnAckAdvanced(sourceID, pos)
			}
		}
	}

	// Close all targets.
	for i := range e.pipelines {
		p := &e.pipelines[i]
		if p.state != PipelineStopped {
			_ = p.target.OnClose(ctx, p.table)
			e.transitionPipeline(i, PipelineStopped)
		}
	}

	// Close all sources.
	for sourceID, source := range e.sources {
		if err := source.Close(ctx); err != nil {
			e.observer.OnSourceDisconnected(sourceID, fmt.Sprintf("close error: %v", err))
		}
	}

	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) AwaitReady(timeout time.Duration) bool {
	return e.readiness.AwaitReady(timeout)
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) IsReady() bool {
	return e.readiness.IsReady()
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) IsSourceReady(sourceID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	found := false
	for i := range e.pipelines {
		p := &e.pipelines[i]
		if p.sourceID != sourceID {
			continue
		}
		found = true
		if p.state != PipelineStreaming && p.state != PipelinePaused {
			return false
		}
	}
	return found
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) OnReady(callback func()) {
	e.readiness.OnReady(callback)
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Reload(ctx context.Context, sourceID string, table TableIdentifier) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.started {
		return errNotStarted
	}
	if e.stopped {
		return errStopped
	}
	source, ok := e.sources[sourceID]
	if !ok {
		return fmt.Errorf("unknown source: %s", sourceID)
	}

	// Find pipelines matching this source+table.
	var affectedIdxs []int
	for i := range e.pipelines {
		p := &e.pipelines[i]
		if p.sourceID == sourceID && p.table == table &&
			(p.state == PipelineStreaming || p.state == PipelinePaused) {
			affectedIdxs = append(affectedIdxs, i)
		}
	}
	if len(affectedIdxs) == 0 {
		return fmt.Errorf("no active pipelines for source %s table %s", sourceID, table)
	}

	// Pause the source to stop change delivery.
	if err := source.Pause(ctx); err != nil {
		return fmt.Errorf("pause source for reload: %w", err)
	}

	// Transition affected pipelines to BASELINING.
	for _, idx := range affectedIdxs {
		e.transitionPipeline(idx, PipelineBaselining)
	}

	// Truncate affected targets.
	for _, idx := range affectedIdxs {
		p := &e.pipelines[idx]
		if err := p.target.OnTruncate(ctx, p.table); err != nil {
			e.transitionPipeline(idx, PipelineError)
		}
	}

	// Fire baseline started observer events.
	for _, idx := range affectedIdxs {
		p := &e.pipelines[idx]
		if p.state == PipelineBaselining {
			e.observer.OnBaselineStarted(p.id, p.table)
		}
	}

	// Re-baseline from source.
	var rowCount int64
	baselineStart := time.Now()

	_, err := source.Baseline(ctx, []TableIdentifier{table}, func(t TableIdentifier, row Row) {
		rowCount++
		e.dispatchBaselineRow(ctx, affectedIdxs, t, row)
	})
	if err != nil {
		// Resume source even on error before returning.
		_ = source.Resume(ctx)
		for _, idx := range affectedIdxs {
			e.transitionPipeline(idx, PipelineError)
		}
		return fmt.Errorf("reload baseline failed: %w", err)
	}

	baselineDuration := time.Since(baselineStart)

	// Notify baseline complete.
	for _, idx := range affectedIdxs {
		p := &e.pipelines[idx]
		if p.state != PipelineBaselining {
			continue
		}
		if err := p.target.OnBaselineComplete(ctx, p.table); err != nil {
			e.transitionPipeline(idx, PipelineError)
			continue
		}
		e.observer.OnBaselineCompleted(p.id, p.table, rowCount, baselineDuration)
	}

	// Transition back to STREAMING.
	for _, idx := range affectedIdxs {
		p := &e.pipelines[idx]
		if p.state == PipelineBaselining {
			e.transitionPipeline(idx, PipelineStreaming)
		}
	}

	// Resume the source.
	if err := source.Resume(ctx); err != nil {
		return fmt.Errorf("resume source after reload: %w", err)
	}

	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Pause(ctx context.Context, sourceID string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.started {
		return errNotStarted
	}
	if e.stopped {
		return errStopped
	}
	source, ok := e.sources[sourceID]
	if !ok {
		return fmt.Errorf("unknown source: %s", sourceID)
	}

	if err := source.Pause(ctx); err != nil {
		return fmt.Errorf("pause source %s: %w", sourceID, err)
	}

	// Transition STREAMING pipelines to PAUSED.
	for i := range e.pipelines {
		p := &e.pipelines[i]
		if p.sourceID == sourceID && p.state == PipelineStreaming {
			e.transitionPipeline(i, PipelinePaused)
		}
	}

	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Resume(ctx context.Context, sourceID string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.started {
		return errNotStarted
	}
	if e.stopped {
		return errStopped
	}
	source, ok := e.sources[sourceID]
	if !ok {
		return fmt.Errorf("unknown source: %s", sourceID)
	}

	if err := source.Resume(ctx); err != nil {
		return fmt.Errorf("resume source %s: %w", sourceID, err)
	}

	// Transition PAUSED pipelines back to STREAMING.
	for i := range e.pipelines {
		p := &e.pipelines[i]
		if p.sourceID == sourceID && p.state == PipelinePaused {
			e.transitionPipeline(i, PipelineStreaming)
		}
	}

	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) CreateSnapshot(ctx context.Context, userMeta map[string]Value) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.started {
		return errNotStarted
	}
	if e.stopped {
		return errStopped
	}
	return e.doSnapshot(ctx, userMeta)
}

// doSnapshot performs the snapshot without lifecycle state checks.
// Caller must hold e.mu.RLock or ensure no concurrent mutations.
func (e *coreEngine) doSnapshot(ctx context.Context, userMeta map[string]Value) error {
	if e.snapshotStore == nil {
		return errors.New("no snapshot store configured")
	}

	snapshotID := fmt.Sprintf("snap-%d", time.Now().UnixNano())

	e.observer.OnSnapshotStarted(snapshotID)
	snapshotStart := time.Now()

	// Pause all sources to get a consistent view.
	for sourceID, source := range e.sources {
		if err := source.Pause(ctx); err != nil {
			e.observer.OnSnapshotFailed(snapshotID, ErrorInfo{Err: err, Message: fmt.Sprintf("pause source %s: %v", sourceID, err)})
			return fmt.Errorf("pause source %s for snapshot: %w", sourceID, err)
		}
	}

	// Export all pipeline targets.
	allEntries := make(map[TableIdentifier][]SnapshotEntry)
	var tableInfos []TableSnapshotInfo
	var totalRows int64

	for i := range e.pipelines {
		p := &e.pipelines[i]
		if p.state != PipelinePaused && p.state != PipelineStreaming {
			continue
		}

		entries, err := p.target.ExportSnapshot(ctx)
		if err != nil {
			// Resume sources before returning.
			for _, src := range e.sources {
				_ = src.Resume(ctx)
			}
			e.observer.OnSnapshotFailed(snapshotID, ErrorInfo{Err: err, Message: fmt.Sprintf("export %s: %v", p.id, err)})
			return fmt.Errorf("export snapshot for pipeline %s: %w", p.id, err)
		}

		allEntries[p.table] = append(allEntries[p.table], entries...)
		totalRows += int64(len(entries))

		tableInfos = append(tableInfos, TableSnapshotInfo{
			Table:      p.table,
			RowCount:   int64(len(entries)),
			TargetType: targetTypeName(p.target),
		})
	}

	// Build metadata.
	metadata := SnapshotMetadata{
		SnapshotID: snapshotID,
		CreatedAt:  time.Now(),
		Tables:     tableInfos,
		UserMeta:   userMeta,
	}

	// Save through the store.
	_, err := e.snapshotStore.Save(ctx, snapshotID, metadata, allEntries)
	if err != nil {
		// Resume sources before returning.
		for _, src := range e.sources {
			_ = src.Resume(ctx)
		}
		e.observer.OnSnapshotFailed(snapshotID, ErrorInfo{Err: err, Message: fmt.Sprintf("save: %v", err)})
		return fmt.Errorf("save snapshot: %w", err)
	}

	// Resume all sources.
	for sourceID, source := range e.sources {
		if err := source.Resume(ctx); err != nil {
			e.observer.OnSnapshotFailed(snapshotID, ErrorInfo{Err: err, Message: fmt.Sprintf("resume source %s: %v", sourceID, err)})
		}
	}

	e.observer.OnSnapshotCompleted(snapshotID, len(tableInfos), totalRows, 0, time.Since(snapshotStart))

	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Targets(sourceID string, table TableIdentifier) []SyncTarget {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var result []SyncTarget
	for i := range e.pipelines {
		p := &e.pipelines[i]
		if p.sourceID == sourceID && p.table == table {
			result = append(result, p.target)
		}
	}
	return result
}

// GetTarget retrieves a typed target from the engine for direct querying.
// It searches for a pipeline matching the given source and table whose target
// can be type-asserted to T. Returns the target and true if found.
func GetTarget[T SyncTarget](e Engine, sourceID string, table TableIdentifier) (T, bool) {
	var zero T
	for _, t := range e.Targets(sourceID, table) {
		if typed, ok := t.(T); ok {
			return typed, true
		}
	}
	return zero, false
}
