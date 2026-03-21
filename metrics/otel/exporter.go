package otel

import (
	"fmt"
	"time"

	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// ExporterType identifies the type of metrics exporter.
type ExporterType string

// Exporter types.
const (
	ExporterStdout ExporterType = "stdout"
)

// NewMeterProvider creates a configured MeterProvider for the given exporter type.
// The returned shutdown function should be called on application exit.
func NewMeterProvider(exporterType ExporterType, interval time.Duration) (*sdkmetric.MeterProvider, func(), error) {
	if interval <= 0 {
		interval = 10 * time.Second
	}

	switch exporterType {
	case ExporterStdout:
		exporter, err := stdoutmetric.New()
		if err != nil {
			return nil, nil, fmt.Errorf("create stdout exporter: %w", err)
		}
		provider := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
				sdkmetric.WithInterval(interval))),
		)
		shutdown := func() { _ = provider.Shutdown(nil) } //nolint:staticcheck // nil context is ok for shutdown
		return provider, shutdown, nil

	default:
		return nil, nil, fmt.Errorf("unsupported exporter type: %q", exporterType)
	}
}
