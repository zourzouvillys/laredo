---
sidebar_position: 1
title: Design Specification
---

# Design Specification

The full design specification for Laredo lives in the repository at [`docs/spec.md`](https://github.com/zourzouvillys/laredo/blob/main/docs/spec.md). This page provides a high-level summary. Refer to the spec for complete details on every interface, behavior, and edge case.

## Purpose

The spec defines the contract between all components of the system: the engine, sources, targets, snapshot stores, filters, transforms, observers, and the gRPC services. It is the authoritative reference for how Laredo is expected to behave.

## Design summary

### Pipeline model

The engine manages a set of **pipelines**. Each pipeline binds a source, a table, and a target together with optional filters and transforms.

```
Pipeline {
    source:     SyncSource      (shared across pipelines)
    table:      TableIdentifier
    filters:    []PipelineFilter
    transforms: []PipelineTransform
    target:     SyncTarget
    buffer:     ChangeBuffer
    errorPolicy: ErrorPolicy
}
```

Sources are instantiated once and shared. If a PostgreSQL source feeds three tables into different targets, that is one source instance, one replication stream, and three pipeline/target pairs. The engine demuxes changes from the source by table and dispatches to the appropriate targets.

### Source/target abstraction

The two core interfaces separate data production from data consumption:

- **SyncSource** provides two capabilities: a point-in-time baseline snapshot and an ordered change stream that picks up from where the snapshot left off. Sources also support ACK/position-tracking semantics so the engine can coordinate durability. Each source defines its own opaque `Position` type (e.g., PostgreSQL LSN, Kinesis sequence number).

- **SyncTarget** receives baseline rows during initial load and then change events (insert, update, delete, truncate) during streaming. Targets report durability status so the engine knows when it is safe to advance the ACK position.

This separation means new sources and targets can be added without modifying the engine.

### ACK coordination

When multiple targets share a source, the engine advances the source ACK position only after **all** targets on that source have confirmed durability. The ACK position is the minimum confirmed position across all pipelines sharing the source. This ensures that if the process restarts, no target will miss changes.

### Startup paths

The engine supports three startup paths:

1. **Cold start** -- no prior state. Performs a full baseline load from the source, then begins streaming.
2. **Resume** -- the source supports resume (`SupportsResume() == true`) and has a valid last-ACKed position. The engine skips the baseline and begins streaming from the saved position.
3. **Snapshot restore** -- a snapshot store is configured and contains a valid snapshot. The engine restores target state from the snapshot, then resumes streaming from the snapshot's source position.

### Error isolation

Each pipeline has its own error policy. A failure in one pipeline does not affect others. The engine supports configurable error policies: isolate (quarantine the failing pipeline), retry with backoff, or dead-letter the failing change and continue.

### Backpressure

Each pipeline has a bounded change buffer between the source demuxer and the target. When the buffer fills, the engine applies the configured buffer policy: block the source (backpressure), drop oldest changes, or drop newest changes.

## Spec contents

The full specification covers:

| Section | Topics |
|---|---|
| Architecture | Module structure, layer diagram, pipeline model, ACK coordination |
| Source interface | Full `SyncSource` contract, position semantics, ordering guarantees, state machine |
| Source implementations | PostgreSQL (ephemeral and stateful modes, publication management), S3 + Kinesis |
| Target interface | Full `SyncTarget` contract, durability, schema changes, snapshots |
| Target implementations | Indexed memory, compiled memory, HTTP sync, replication fan-out |
| Snapshot system | Store and serializer interfaces, scheduling, retention, restore |
| Pipeline components | Filters, transforms, buffer policies, error policies, TTL |
| Engine lifecycle | Startup paths, readiness signaling, graceful shutdown, hot reload |
| Observer interface | All event types, metrics bridge contract |
| gRPC services | OAM, Query, and Replication service definitions |
| Fan-out protocol | Journal management, snapshot creation, client sync modes, catch-up |
| Configuration | HOCON schema, resolution order, environment variable mapping |
| CLI tool | All subcommands and their behavior |

## Reading the spec

The spec uses pseudocode for interface definitions (not Go syntax). The naming conventions in the spec (`snake_case`, `tablesync`) differ from the Go implementation (`CamelCase`, `laredo`). The mapping is straightforward:

| Spec name | Go name |
|---|---|
| `tablesync-core` | `github.com/zourzouvillys/laredo` |
| `tablesync-server` | `laredo-server` |
| `tsync` | `laredo` (CLI) |
| `sync_source` | `SyncSource` |
| `sync_target` | `SyncTarget` |
| `engine_observer` | `EngineObserver` |

## Further reading

- [Architecture](/concepts/architecture) -- high-level overview of the three-layer design
- [Pipelines](/concepts/pipelines) -- pipeline model concepts
- [Architecture Decision Records](/internals/adrs) -- key design choices and rationale
