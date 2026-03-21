package laredo

// ValidationError is returned when source validation fails.
type ValidationError struct {
	Table   *TableIdentifier // nil for source-level errors
	Code    string           // e.g. "TABLE_NOT_FOUND", "PERMISSION_DENIED"
	Message string
}

// Error implements the error interface.
func (e ValidationError) Error() string {
	if e.Table != nil {
		return e.Table.String() + ": " + e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message
}

// BufferPolicy controls behavior when a pipeline's change buffer is full.
type BufferPolicy int

// Buffer overflow policies.
const (
	BufferBlock      BufferPolicy = iota // Block the source dispatcher.
	BufferDropOldest                     // Drop the oldest undelivered change.
	BufferError                          // Mark the pipeline as ERROR.
)

// ErrorPolicyKind controls behavior on persistent pipeline failure.
type ErrorPolicyKind int

// Error handling policies.
const (
	ErrorIsolate    ErrorPolicyKind = iota // Isolate the failed pipeline, continue others.
	ErrorStopSource                        // Stop all pipelines on the source.
	ErrorStopAll                           // Halt the engine.
)

// ValidationAction controls what happens when a validation check detects a mismatch.
type ValidationAction int

// Validation mismatch actions.
const (
	ValidationWarn       ValidationAction = iota // Log via observer, continue streaming.
	ValidationReBaseline                         // Trigger re-baseline for the table.
	ValidationFail                               // Mark pipeline as ERROR.
)

// DeadLetterStore persists failed changes for later inspection or replay.
type DeadLetterStore interface {
	// Write persists a failed change with error context.
	Write(pipelineID string, change ChangeEvent, err ErrorInfo) error
}
