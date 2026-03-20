---
sidebar_position: 4
title: Metrics
---

# Metrics Reference

All metrics are exported via Prometheus at `/metrics` on the configured port (default: 9090).

## Pipeline metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `laredo_pipeline_state` | Gauge | `pipeline_id`, `source_id`, `table` | Current state (enum) |
| `laredo_pipeline_row_count` | Gauge | `pipeline_id`, `table` | Rows in target |
| `laredo_pipeline_buffer_depth` | Gauge | `pipeline_id` | Current buffer depth |
| `laredo_pipeline_buffer_capacity` | Gauge | `pipeline_id` | Buffer capacity |
| `laredo_pipeline_inserts_total` | Counter | `pipeline_id`, `table` | Total inserts applied |
| `laredo_pipeline_updates_total` | Counter | `pipeline_id`, `table` | Total updates applied |
| `laredo_pipeline_deletes_total` | Counter | `pipeline_id`, `table` | Total deletes applied |
| `laredo_pipeline_truncates_total` | Counter | `pipeline_id`, `table` | Total truncates applied |
| `laredo_pipeline_errors_total` | Counter | `pipeline_id`, `table` | Total change errors |
| `laredo_pipeline_dead_letters_total` | Counter | `pipeline_id` | Dead letters written |
| `laredo_pipeline_rows_expired_total` | Counter | `pipeline_id`, `table` | Rows expired by TTL |

## Source metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `laredo_source_lag_bytes` | Gauge | `source_id` | Replication lag in bytes |
| `laredo_source_lag_seconds` | Gauge | `source_id` | Replication lag in seconds |
| `laredo_source_connected` | Gauge | `source_id` | 1 if connected, 0 if not |

## Timing metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `laredo_change_apply_duration_seconds` | Histogram | `pipeline_id`, `action` | Time to apply a single change |
| `laredo_baseline_duration_seconds` | Histogram | `pipeline_id` | Total baseline loading time |
| `laredo_snapshot_duration_seconds` | Histogram | | Time to create a snapshot |

## Fan-out metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `laredo_fanout_connected_clients` | Gauge | `pipeline_id` | Connected fan-out clients |
| `laredo_fanout_journal_entries` | Gauge | `pipeline_id` | Journal entries retained |
| `laredo_fanout_journal_oldest_age_seconds` | Gauge | `pipeline_id` | Age of oldest journal entry |
| `laredo_fanout_snapshots_total` | Counter | `pipeline_id` | Fan-out snapshots created |

## Snapshot metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `laredo_snapshots_created_total` | Counter | | Total snapshots created |
| `laredo_snapshots_failed_total` | Counter | | Total snapshot failures |
| `laredo_snapshots_restored_total` | Counter | | Total snapshot restores |
