package snapshotter

import "context"

// EventSink publishes an advisory, at-least-once notification after the manifest
// head advances. Sinks are off the durability path: a failed publish is logged
// and retried but never blocks or rolls back a written artifact. Consumers must
// tolerate missing or duplicated events and fall back to polling the manifest.
type EventSink interface {
	// Name identifies the sink in logs and metrics.
	Name() string
	// Publish sends an artifact event. Best-effort.
	Publish(ctx context.Context, ev ArtifactEvent) error
}

// NopEventSink discards events. It is the default when no sinks are configured.
type NopEventSink struct{}

// Name implements EventSink.
func (NopEventSink) Name() string { return "nop" }

// Publish implements EventSink.
func (NopEventSink) Publish(context.Context, ArtifactEvent) error { return nil }
