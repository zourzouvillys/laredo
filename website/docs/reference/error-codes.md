---
sidebar_position: 7
title: Error Codes
---

# Error Codes Reference

This page documents the errors returned by the laredo engine, config loader, and gRPC services. Use this as a diagnostic reference when troubleshooting failures.

## Engine validation errors

`NewEngine()` validates the configuration and returns a list of errors if anything is misconfigured. The engine will not start until all validation errors are resolved.

### Source errors

| Error | Cause | Fix |
|---|---|---|
| `at least one source is required` | No sources were registered via `WithSource()`. | Add at least one source to the config or engine options. |
| `source ID must not be empty` | A source was registered with an empty string ID. | Provide a non-empty source ID (e.g., `pg_main`). |
| `source "<id>" must not be nil` | A source was registered with a nil implementation. | Pass a valid `SyncSource` implementation. |

### Pipeline errors

| Error | Cause | Fix |
|---|---|---|
| `at least one pipeline is required` | No pipelines were registered via `WithPipeline()`. | Add at least one pipeline binding a source, table, and target. |
| `pipeline[N]: source ID must not be empty` | A pipeline has no source ID. | Set the `source` field in the table config. |
| `pipeline[N]: references unknown source "<id>"` | A pipeline references a source ID that was not registered. | Check that the source ID in the table config matches a configured source. |
| `pipeline[N]: table name must not be empty` | A pipeline has no table name. | Set the `table` field in the table config. |
| `pipeline[N]: target must not be nil` | A pipeline has a nil target. | Configure a valid target (e.g., `indexed-memory`, `http-sync`). |
| `pipeline[N]: buffer size must be positive, got <n>` | Buffer size is zero or negative. | Set `buffer.max_size` to a positive integer, or omit it to use the default. |
| `pipeline[N]: max retries must be non-negative, got <n>` | Max retries is negative. | Set `error_handling.max_retries` to zero or a positive integer. |
| `pipeline[N]: duplicate pipeline ID "<id>"` | Two pipelines have the same source, table, and target type combination. | Each pipeline must have a unique combination of source ID, table, and target type. |

### Snapshot errors

| Error | Cause | Fix |
|---|---|---|
| `snapshot schedule must not be negative` | Snapshot schedule duration is negative. | Set `snapshot.schedule` to a positive duration or omit it. |
| `snapshot serializer is required when snapshot store is configured` | A snapshot store was provided but no serializer. | Configure a snapshot serializer (e.g., `jsonl`) alongside the store. |

## Config validation errors

`Config.Validate()` checks the HOCON config structure before building engine options.

| Error | Cause | Fix |
|---|---|---|
| `at least one source is required` | The `sources` block is empty or missing. | Add at least one source to the config file. |
| `at least one table is required` | The `tables` array is empty or missing. | Add at least one table entry to the config file. |
| `tables[N]: source is required` | A table entry has no `source` field. | Set the `source` field to match a configured source ID. |
| `tables[N]: references unknown source "<id>"` | A table references a source not defined in `sources`. | Add the source to the `sources` block, or fix the source ID. |
| `tables[N]: schema is required` | A table entry has no `schema` field. | Set the `schema` field (e.g., `public`). |
| `tables[N]: table is required` | A table entry has no `table` field. | Set the `table` field (e.g., `config_document`). |
| `tables[N]: at least one target is required` | A table entry has an empty `targets` array. | Add at least one target to the table entry. |
| `tables[N].targets[M]: type is required` | A target entry has no `type` field. | Set the target `type` (e.g., `indexed-memory`, `http-sync`). |

## Source validation errors

Sources implement `ValidateTables()` which returns `ValidationError` values with structured error codes.

### `ValidationError` structure

```go
type ValidationError struct {
    Table   *TableIdentifier  // nil for source-level errors
    Code    string            // e.g. "TABLE_NOT_FOUND"
    Message string            // human-readable description
}
```

### Known error codes

| Code | Scope | Meaning |
|---|---|---|
| `TABLE_NOT_FOUND` | Table | The requested table does not exist in the source database. |
| `PERMISSION_DENIED` | Source | The source credentials lack required permissions (e.g., replication privilege). |

These errors are returned during engine startup when validating that configured tables exist and are accessible.

## gRPC error codes

The laredo gRPC services use standard [Connect/gRPC status codes](https://connectrpc.com/docs/protocol/#error-codes). Each service returns specific codes depending on the failure.

### Error code summary

| Code | Numeric | When returned |
|---|---|---|
| `InvalidArgument` | 3 | Required fields are missing or have invalid values. |
| `NotFound` | 5 | The requested resource (pipeline, source, target, snapshot, replay) does not exist. |
| `FailedPrecondition` | 9 | An operation requires a precondition that is not met (e.g., no snapshot store, missing confirmation). |
| `ResourceExhausted` | 8 | A resource limit has been reached (e.g., max replication clients). |
| `Internal` | 13 | An unexpected server-side error occurred. |
| `Unimplemented` | 12 | The RPC is not implemented. |

### OAM service errors

#### GetTableStatus, GetTableSchema

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `schema` or `table` is empty | `schema and table are required` |
| `NotFound` | No schema data for the table | `no schema for <schema>.<table>` |

#### GetPipelineStatus

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `pipeline_id` is empty | `pipeline_id is required` |
| `NotFound` | Pipeline ID not found | `pipeline <id> not found` |

#### GetSourceInfo

| Code | Condition | Message |
|---|---|---|
| `NotFound` | Source ID not found | `source <id> not found` |

#### ReloadTable

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `source_id` is empty | `source_id is required` |
| `InvalidArgument` | Invalid table identifier | `invalid table: <error>` |

#### PauseSync, ResumeSync

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `source_id` is empty | `source_id is required` |
| `Internal` | Engine operation failed | (error from engine) |

#### ResetSource

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `source_id` is empty | `source_id is required` |
| `FailedPrecondition` | `confirm` is not `true` | `confirm=true is required for destructive operation` |

#### CreateSnapshot

No specific error codes. If the engine rejects the snapshot, the response contains `accepted=false` with a message.

#### ListSnapshots, InspectSnapshot, DeleteSnapshot, PruneSnapshots

| Code | Condition | Message |
|---|---|---|
| `FailedPrecondition` | No snapshot store configured | `no snapshot store configured` |
| `InvalidArgument` | `snapshot_id` is empty (Inspect, Delete) | `snapshot_id is required` |
| `InvalidArgument` | `keep` is not positive (Prune) | `keep must be positive` |
| `NotFound` | Snapshot not found (Inspect) | `describe snapshot: <error>` |
| `Internal` | Store operation failed | `list snapshots: <error>`, `delete snapshot: <error>`, `prune snapshots: <error>` |

#### StartReplay, GetReplayStatus, StopReplay

| Code | Condition | Message |
|---|---|---|
| `FailedPrecondition` | No snapshot store configured | `no snapshot store configured` |
| `InvalidArgument` | `snapshot_id` is empty | `snapshot_id is required` |
| `InvalidArgument` | Invalid table identifier | `invalid table "<table>": <error>` |
| `NotFound` | Replay ID not found | `replay <id> not found` |

#### ListDeadLetters, PurgeDeadLetters

| Code | Condition | Message |
|---|---|---|
| `FailedPrecondition` | No dead letter store configured | `no dead letter store configured` |
| `InvalidArgument` | `pipeline_id` is empty | `pipeline_id is required` |
| `Internal` | Store operation failed | `read dead letters: <error>`, `purge dead letters: <error>` |

### Query service errors

#### Lookup, GetRow, ListRows, CountRows, Subscribe

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `schema` or `table` is empty | `schema and table are required` |
| `NotFound` | No indexed target for the table | `no indexed target found for <schema>.<table>` |
| `Internal` | Row serialization failed | (serialization error) |

#### LookupAll

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `schema` or `table` is empty | `schema and table are required` |
| `InvalidArgument` | `index_name` is empty | `index_name is required` |
| `NotFound` | No indexed target for the table | `no indexed target found for <schema>.<table>` |
| `Internal` | Row serialization failed | (serialization error) |

### Replication service errors

#### GetReplicationStatus, ListSnapshots (replication)

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `schema` or `table` is empty | `schema and table are required` |
| `NotFound` | No fan-out target for the table | `no fan-out target for <schema>.<table>` |

#### FetchSnapshot

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `snapshot_id` is empty | `snapshot_id is required` |
| `NotFound` | Snapshot not found | `snapshot <id> not found` |

#### Sync

| Code | Condition | Message |
|---|---|---|
| `InvalidArgument` | `schema` or `table` is empty | `schema and table are required` |
| `NotFound` | No fan-out target for the table | `no fan-out target for <schema>.<table>` |
| `ResourceExhausted` | Max connected clients reached | `max clients reached` |

## Troubleshooting common errors

### "at least one source is required"

The config file has no `sources` block, or the block is empty. Add a source:

```hocon
sources {
  pg_main {
    type = postgresql
    connection = "postgresql://user:pass@localhost:5432/db"
  }
}
```

### "references unknown source"

A table's `source` field does not match any key in the `sources` block. Check for typos:

```hocon
sources {
  pg_main { ... }    # <-- source ID
}
tables = [{
  source = pg_main   # <-- must match
  ...
}]
```

### "no snapshot store configured"

A snapshot operation was requested (via CLI or gRPC), but the server was not configured with a snapshot store. Add snapshot configuration:

```hocon
snapshot {
  enabled = true
  store = local
  store_config { path = "/var/lib/laredo/snapshots" }
  serializer = jsonl
}
```

### "confirm=true is required for destructive operation"

The `ResetSource` RPC requires explicit confirmation because it drops and recreates the replication slot. Pass `--confirm` when using the CLI:

```bash
laredo reset-source pg_main --confirm
```

### "max clients reached"

The fan-out target has reached its maximum number of connected replication clients. Increase the limit in the target config or disconnect idle clients:

```hocon
targets = [{
  type = replication-fanout
  grpc { max_clients = 1000 }
}]
```

### "no indexed target found" / "no fan-out target"

A query or replication RPC was called for a table that does not have the expected target type. Verify that the table has an `indexed-memory` target (for query RPCs) or a `replication-fanout` target (for replication RPCs) configured.
