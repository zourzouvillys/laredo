---
sidebar_position: 8
title: Monitoring
---

# Monitoring

Laredo's core library emits structured events through the `EngineObserver` interface. The pre-built service includes Prometheus and OpenTelemetry observers.

## EngineObserver

The observer receives callbacks for every lifecycle event:

- Source connected/disconnected
- Pipeline state changes
- Baseline progress and completion
- Change received/applied/error
- ACK advances
- Buffer depth changes
- Snapshot creation/restore
- Schema changes
- Lag updates
- Dead letter writes
- Row expirations
- Fan-out client events

## Prometheus

```hocon
observability {
  metrics {
    type = prometheus
    port = 9090
    path = "/metrics"
  }
}
```

### Key metrics

| Metric | Type | Description |
|---|---|---|
| `laredo_pipeline_state` | Gauge | Current pipeline state |
| `laredo_pipeline_row_count` | Gauge | Rows in target |
| `laredo_pipeline_buffer_depth` | Gauge | Current buffer depth |
| `laredo_pipeline_inserts_total` | Counter | Total inserts applied |
| `laredo_pipeline_updates_total` | Counter | Total updates applied |
| `laredo_pipeline_deletes_total` | Counter | Total deletes applied |
| `laredo_pipeline_errors_total` | Counter | Total errors |
| `laredo_source_lag_bytes` | Gauge | Replication lag in bytes |
| `laredo_source_lag_seconds` | Gauge | Replication lag in seconds |
| `laredo_change_apply_duration_seconds` | Histogram | Time to apply a change |
| `laredo_baseline_duration_seconds` | Histogram | Time to complete baseline |
| `laredo_snapshot_duration_seconds` | Histogram | Time to create a snapshot |
| `laredo_fanout_connected_clients` | Gauge | Connected fan-out clients |

## OpenTelemetry

```hocon
observability {
  metrics {
    type = otel
  }
}
```

Maps the same metrics to the OTel meter API with configurable exporters (OTLP, stdout, etc.).

## Custom observer

Implement the `EngineObserver` interface for custom observability:

```go
engine, _ := laredo.NewEngine(
    // ...
    laredo.WithObserver(myCustomObserver),
)
```

Use `CompositeObserver` to fan out to multiple observers simultaneously.
