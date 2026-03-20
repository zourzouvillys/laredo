package laredo

// PipelineFilter determines whether a row should be included in the pipeline.
type PipelineFilter interface {
	// Include returns true if the row should be passed to the target, false to skip.
	Include(table TableIdentifier, row Row) bool
}

// PipelineFilterFunc adapts a function to the PipelineFilter interface.
type PipelineFilterFunc func(TableIdentifier, Row) bool

// Include implements PipelineFilter.
func (f PipelineFilterFunc) Include(table TableIdentifier, row Row) bool {
	return f(table, row)
}

// PipelineTransform modifies a row before it reaches the target.
type PipelineTransform interface {
	// Transform modifies a row. Return nil to drop the row entirely.
	Transform(table TableIdentifier, row Row) Row
}

// PipelineTransformFunc adapts a function to the PipelineTransform interface.
type PipelineTransformFunc func(TableIdentifier, Row) Row

// Transform implements PipelineTransform.
func (f PipelineTransformFunc) Transform(table TableIdentifier, row Row) Row {
	return f(table, row)
}

// PipelineState represents the lifecycle state of a pipeline.
type PipelineState int

const (
	PipelineInitializing PipelineState = iota
	PipelineBaselining
	PipelineStreaming
	PipelinePaused
	PipelineError
	PipelineStopped
)

// String returns the state name.
func (s PipelineState) String() string {
	switch s {
	case PipelineInitializing:
		return "INITIALIZING"
	case PipelineBaselining:
		return "BASELINING"
	case PipelineStreaming:
		return "STREAMING"
	case PipelinePaused:
		return "PAUSED"
	case PipelineError:
		return "ERROR"
	case PipelineStopped:
		return "STOPPED"
	default:
		return "UNKNOWN"
	}
}
