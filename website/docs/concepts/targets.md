---
sidebar_position: 4
title: Targets
---

# Targets

A target consumes baseline rows and change events for a table. Different targets serve different use cases.

## SyncTarget interface

```go
type SyncTarget interface {
    OnInit(ctx context.Context, table TableIdentifier, columns []ColumnDefinition) error
    OnBaselineRow(ctx context.Context, table TableIdentifier, row Row) error
    OnBaselineComplete(ctx context.Context, table TableIdentifier) error
    OnInsert(ctx context.Context, table TableIdentifier, columns Row) error
    OnUpdate(ctx context.Context, table TableIdentifier, columns Row, identity Row) error
    OnDelete(ctx context.Context, table TableIdentifier, identity Row) error
    OnTruncate(ctx context.Context, table TableIdentifier) error
    IsDurable() bool
    OnSchemaChange(ctx context.Context, table TableIdentifier, oldColumns, newColumns []ColumnDefinition) SchemaChangeResponse
    ExportSnapshot(ctx context.Context) ([]SnapshotEntry, error)
    RestoreSnapshot(ctx context.Context, metadata TableSnapshotInfo, entries []SnapshotEntry) error
    OnClose(ctx context.Context, table TableIdentifier) error
}
```

## Available targets

### Indexed In-Memory

Schema-agnostic in-memory table replica with configurable secondary indexes. Stores raw rows. Best for general-purpose lookups.

- `IsDurable()`: always `true` (memory is the store)
- Query API: `Lookup()`, `LookupAll()`, `Get()`, `All()`, `Count()`, `Listen()`

### Compiled In-Memory

Deserializes each row into a strongly-typed domain object via a pluggable compiler function. Best when you need typed data.

- Compiler: `func(Row) (any, error)`
- `IsDurable()`: always `true`

### HTTP Sync

Forwards every row change to a downstream HTTP service as batched POST requests. The downstream owns the data; this target is stateless.

- Batches baseline rows and changes (configurable batch size and flush interval)
- `IsDurable()`: `true` only after batch flush with 2xx HTTP response

### Replication Fan-Out

Multiplexes one source to N downstream gRPC clients. Maintains in-memory state, a bounded change journal, and periodic snapshots. Clients connect and receive a consistent snapshot followed by a live change stream.

- `IsDurable()`: always `true`
- Conceptually similar to Netflix Hollow

## Durability and ACK

The `IsDurable()` method controls when the engine ACKs the source:

- **In-memory targets** return `true` immediately — memory is the store
- **HTTP sync** returns `true` only after the downstream confirms receipt
- The engine ACKs the minimum durable position across all targets sharing a source
