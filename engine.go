package laredo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
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
	for i, pc := range cfg.pipelines {
		pipelines[i] = resolvedPipeline{
			id:           generatePipelineID(pc.sourceID, pc.table, pc.target),
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
	}

	return &coreEngine{
		sources:            cfg.sources,
		pipelines:          pipelines,
		observer:           observer,
		snapshotStore:      cfg.snapshotStore,
		snapshotSerializer: cfg.snapshotSerializer,
		snapshotSchedule:   cfg.snapshotSchedule,
		snapshotOnShutdown: cfg.snapshotOnShutdown,
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
	sources   map[string]SyncSource
	pipelines []resolvedPipeline
	observer  EngineObserver

	snapshotStore      SnapshotStore
	snapshotSerializer SnapshotSerializer
	snapshotSchedule   time.Duration
	snapshotOnShutdown bool

	mu      sync.RWMutex
	started bool
	stopped bool
}

var _ Engine = (*coreEngine)(nil)

//nolint:revive // Engine interface implementation; docs are on the interface.
func (e *coreEngine) Start(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return errStopped
	}
	if e.started {
		return errAlreadyStarted
	}
	e.started = true
	// TODO: implement pipeline startup
	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Stop(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return errStopped
	}
	if !e.started {
		return errNotStarted
	}
	e.stopped = true
	// TODO: implement graceful shutdown
	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) AwaitReady(_ time.Duration) bool {
	// TODO: implement readiness tracking
	return false
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) IsReady() bool {
	// TODO: implement readiness tracking
	return false
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Reload(_ context.Context, sourceID string, _ TableIdentifier) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.started {
		return errNotStarted
	}
	if e.stopped {
		return errStopped
	}
	if _, ok := e.sources[sourceID]; !ok {
		return fmt.Errorf("unknown source: %s", sourceID)
	}
	// TODO: implement re-baseline
	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Pause(_ context.Context, sourceID string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.started {
		return errNotStarted
	}
	if e.stopped {
		return errStopped
	}
	if _, ok := e.sources[sourceID]; !ok {
		return fmt.Errorf("unknown source: %s", sourceID)
	}
	// TODO: implement pause
	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) Resume(_ context.Context, sourceID string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.started {
		return errNotStarted
	}
	if e.stopped {
		return errStopped
	}
	if _, ok := e.sources[sourceID]; !ok {
		return fmt.Errorf("unknown source: %s", sourceID)
	}
	// TODO: implement resume
	return nil
}

//nolint:revive // Engine interface implementation.
func (e *coreEngine) CreateSnapshot(_ context.Context, _ map[string]Value) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.started {
		return errNotStarted
	}
	if e.stopped {
		return errStopped
	}
	if e.snapshotStore == nil {
		return errors.New("no snapshot store configured")
	}
	// TODO: implement snapshot creation
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
