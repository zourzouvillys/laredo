---
sidebar_position: 3
title: Sources
---

# Sources

A source provides two capabilities: a point-in-time baseline snapshot and an ordered change stream that picks up where the snapshot left off.

## SyncSource interface

Every source implements the `SyncSource` interface:

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
    Pause(ctx context.Context) error
    Resume(ctx context.Context) error
    GetLag() LagInfo
    OrderingGuarantee() OrderingGuarantee
    State() SourceState
    Close(ctx context.Context) error
}
```

## Position

The `Position` type is opaque to the engine. Each source defines what it means:

- **PostgreSQL**: LSN (Log Sequence Number)
- **Kinesis**: composite of S3 version + per-shard sequence numbers

The engine uses `ComparePositions` for ACK coordination — it ACKs the minimum confirmed position across all pipelines sharing a source.

## Available sources

| Source | Module | Ordering | Resume |
|---|---|---|---|
| PostgreSQL | `source/pg` | Total order | Stateful mode only |
| S3 + Kinesis | `source/kinesis` | Per-partition | With checkpointing |
| Test (in-memory) | `source/testsource` | Total order | No |

## Source states

```
CONNECTING ──► CONNECTED ──► STREAMING
     ▲              │              │
     │              │         (connection lost)
     │              ▼              ▼
     │         RECONNECTING ◄─────┘
     │              │
     │         (max retries exceeded)
     │              ▼
     └──────── ERROR
```

## Ephemeral vs. stateful

Whether a source can resume from a previously ACKed position is a property of the source configuration:

- **Ephemeral**: every startup requires a full baseline. Simple, no state to manage.
- **Stateful**: resume from the last ACKed position. No data reprocessing on restart, but requires persistent position tracking (e.g., a named PostgreSQL replication slot).
