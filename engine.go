package laredo

import (
	"context"
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
	sourceID   string
	table      TableIdentifier
	target     SyncTarget
	filters    []PipelineFilter
	transforms []PipelineTransform
	bufferSize int
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
			sourceID:   sourceID,
			table:      table,
			target:     target,
			bufferSize: 10000,
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
	// TODO: implement
	return nil, nil
}
