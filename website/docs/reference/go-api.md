---
sidebar_position: 8
title: Go Library API
---

# Go Library API Reference

Full generated documentation is available on pkg.go.dev:

**[pkg.go.dev/github.com/zourzouvillys/laredo](https://pkg.go.dev/github.com/zourzouvillys/laredo)**

This page summarizes the key packages and interfaces. For method signatures, types, and detailed godoc, see the link above.

## Packages

### Core

| Package | Import path | Description |
|---|---|---|
| `laredo` (root) | `github.com/zourzouvillys/laredo` | All public interfaces, types, builder options, and `NewEngine()` |

### Sources

| Package | Import path | Description |
|---|---|---|
| `source/pg` | `github.com/zourzouvillys/laredo/source/pg` | PostgreSQL logical replication source (ephemeral and stateful modes, publication management, reconnection) |
| `source/kinesis` | `github.com/zourzouvillys/laredo/source/kinesis` | S3 baseline + Kinesis change stream source |

### Targets

| Package | Import path | Description |
|---|---|---|
| `target/memory` | `github.com/zourzouvillys/laredo/target/memory` | `IndexedTarget` (raw rows with secondary indexes) and `CompiledTarget` (domain objects via compiler function) |
| `target/fanout` | `github.com/zourzouvillys/laredo/target/fanout` | Replication fan-out target with in-memory state, change journal, snapshots, and embedded gRPC server |
| `target/httpsync` | `github.com/zourzouvillys/laredo/target/httpsync` | HTTP sync target (batched POST, retry, durability tracking) |

### Snapshots

| Package | Import path | Description |
|---|---|---|
| `snapshot/local` | `github.com/zourzouvillys/laredo/snapshot/local` | Local disk snapshot store |
| `snapshot/s3` | `github.com/zourzouvillys/laredo/snapshot/s3` | S3 snapshot store |
| `snapshot/jsonl` | `github.com/zourzouvillys/laredo/snapshot/jsonl` | JSONL snapshot serializer |

### Pipeline components

| Package | Import path | Description |
|---|---|---|
| `filter` | `github.com/zourzouvillys/laredo/filter` | Built-in pipeline filters: `FieldEquals`, `FieldPrefix`, `FieldRegex` |
| `transform` | `github.com/zourzouvillys/laredo/transform` | Built-in pipeline transforms: `DropFields`, `RenameFields`, `AddTimestamp` |
| `deadletter` | `github.com/zourzouvillys/laredo/deadletter` | Dead letter store interface and implementations |

### Services

| Package | Import path | Description |
|---|---|---|
| `service` | `github.com/zourzouvillys/laredo/service` | gRPC server setup (Connect protocol) |
| `service/oam` | `github.com/zourzouvillys/laredo/service/oam` | OAM gRPC service (status, admin, snapshot management, dead letters, replay) |
| `service/query` | `github.com/zourzouvillys/laredo/service/query` | Query gRPC service (lookup, list, count, subscribe) |
| `service/replication` | `github.com/zourzouvillys/laredo/service/replication` | Fan-out replication gRPC service |

### Clients

| Package | Import path | Description |
|---|---|---|
| `client/fanout` | `github.com/zourzouvillys/laredo/client/fanout` | Go client for the replication fan-out protocol |

### Configuration

| Package | Import path | Description |
|---|---|---|
| `config` | `github.com/zourzouvillys/laredo/config` | HOCON config loading and validation |

## Core interfaces

### Engine

`Engine` is the top-level orchestrator. It manages pipelines, coordinates startup (baseline or resume), streams changes, and handles shutdown.

```go
type Engine interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    AwaitReady(timeout time.Duration) bool
    IsReady() bool
    IsSourceReady(sourceID string) bool
    OnReady(callback func())
    Reload(ctx context.Context, sourceID string, table TableIdentifier) error
    Pause(ctx context.Context, sourceID string) error
    Resume(ctx context.Context, sourceID string) error
    CreateSnapshot(ctx context.Context, userMeta map[string]Value) error
    Targets(sourceID string, table TableIdentifier) []SyncTarget
    Pipelines() []PipelineInfo
    SourceIDs() []string
    SourceInfo(sourceID string) (SourceRunInfo, bool)
    TableSchema(table TableIdentifier) []ColumnDefinition
    ResetSource(ctx context.Context, sourceID string, dropPublication bool) error
}
```

Create an engine with `NewEngine()` and a set of `Option` values. The engine does not start until `Start()` is called.

### SyncSource

`SyncSource` produces baseline snapshots and change streams from a data source. Sources are shared across pipelines -- one PostgreSQL source can feed multiple tables into different targets.

```go
type SyncSource interface {
    Init(ctx context.Context, config SourceConfig) (map[TableIdentifier][]ColumnDefinition, error)
    ValidateTables(ctx context.Context, tables []TableIdentifier) []ValidationError
    Baseline(ctx context.Context, tables []TableIdentifier, rowCallback func(TableIdentifier, Row)) (Position, error)
    Stream(ctx context.Context, from Position, handler ChangeHandler) error
    Ack(ctx context.Context, position Position) error
    SupportsResume() bool
    LastAckedPosition(ctx context.Context) (Position, error)
    ComparePositions(a, b Position) int
    PositionToString(p Position) string
    PositionFromString(s string) (Position, error)
    Pause(ctx context.Context) error
    Resume(ctx context.Context) error
    GetLag() LagInfo
    OrderingGuarantee() OrderingGuarantee
    State() SourceState
    Close(ctx context.Context) error
}
```

### SyncTarget

`SyncTarget` consumes baseline rows and change events for a single table. Each pipeline has exactly one target instance.

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
    SupportsConsistentSnapshot() bool
    OnClose(ctx context.Context, table TableIdentifier) error
}
```

### EngineObserver

`EngineObserver` receives structured lifecycle and operational events from the engine. All methods are called synchronously from the engine goroutine and must not block.

The interface covers: source lifecycle, baseline progress, change streaming, ACK advancement, backpressure, snapshots, schema changes, lag updates, dead letters, TTL expiry, validation, and fan-out replication events.

Laredo provides two built-in implementations:

- `NullObserver` -- a no-op implementation for embedded users who do not need observability
- `CompositeObserver` -- fans out observer calls to multiple implementations

The `metrics/prometheus` and `metrics/otel` packages provide production-ready observer implementations.

## Example: creating an engine

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/zourzouvillys/laredo"
    "github.com/zourzouvillys/laredo/source/pg"
    "github.com/zourzouvillys/laredo/target/memory"
)

func main() {
    engine, errs := laredo.NewEngine(
        laredo.WithSource("pg_main", pg.New(
            pg.Connection("postgresql://user:pass@localhost:5432/mydb"),
            pg.SlotMode(pg.Ephemeral),
        )),
        laredo.WithPipeline("pg_main",
            laredo.Table("public", "users"),
            memory.NewIndexedTarget(
                memory.LookupFields("id"),
            ),
        ),
    )
    if len(errs) > 0 {
        log.Fatalf("config errors: %v", errs)
    }

    ctx := context.Background()
    engine.Start(ctx)
    defer engine.Stop(ctx)

    engine.AwaitReady(30 * time.Second)
}
```

## Example: querying a target

Use `GetTarget` to retrieve a typed target from a running engine:

```go
target, ok := laredo.GetTarget[*memory.IndexedTarget](
    engine, "pg_main", laredo.Table("public", "users"),
)
if !ok {
    log.Fatal("target not found")
}

// Lookup by primary key fields
row, found := target.Lookup("user-123")
if found {
    log.Printf("name: %s", row.GetString("name"))
}

// List all rows
for _, row := range target.All() {
    log.Printf("row: %v", row)
}

// Subscribe to live changes
target.Listen(func(old, new laredo.Row) {
    log.Printf("changed: %v -> %v", old, new)
})
```

## Further reading

- [Library Usage](/getting-started/library-usage) -- getting started with the Go library
- [In-Memory Targets](/guides/in-memory-targets) -- indexed and compiled target guides
- [Filters and Transforms](/guides/filters-and-transforms) -- pipeline component guide
- [Configuration Reference](/reference/configuration) -- HOCON config for `laredo-server`
