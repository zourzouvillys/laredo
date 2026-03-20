---
sidebar_position: 2
title: Pipelines
---

# Pipelines

A pipeline is the core unit of work in Laredo. Each pipeline binds one source table to one target, with optional filters and transforms in between.

## Pipeline structure

```
Pipeline {
    id:          "pg_main:public.config_document:indexed-memory"
    source:      SyncSource      (shared across pipelines)
    table:       TableIdentifier
    filters:     []PipelineFilter
    transforms:  []PipelineTransform
    target:      SyncTarget
    buffer:      ChangeBuffer    (bounded, per-pipeline)
    errorPolicy: ErrorPolicy
    ttlPolicy:   TtlPolicy       (optional)
}
```

## Source sharing

Sources are instantiated once and shared. If a PostgreSQL source feeds three tables into different targets, that is **one source instance**, one replication stream, and three target instances. The engine demuxes changes from the source by table and dispatches to the correct pipelines.

## Fan-out

A single table can fan out to multiple targets. When this happens, the engine delivers each change to all targets for that table. Source ACK advances only after **all** targets for that source have confirmed `IsDurable()`.

```
PostgreSQL (1 slot)
     │
     ▼ stream demux by table
     │
     ├──► Pipeline: config_document → indexed-memory
     ├──► Pipeline: config_document → http-sync
     └──► Pipeline: config_document → replication-fanout
```

## Pipeline states

Each pipeline has an independent lifecycle:

| State | Meaning |
|---|---|
| `INITIALIZING` | Preparing to start |
| `BASELINING` | Loading initial snapshot from source |
| `STREAMING` | Receiving and applying live changes |
| `PAUSED` | Paused by operator request |
| `ERROR` | Unrecoverable error (isolated from others) |
| `STOPPED` | Shut down |

## Buffer policies

Each pipeline has a bounded change buffer between the source dispatcher and the target:

| Policy | Behavior |
|---|---|
| `block` | Backpressure to source (safe default) |
| `drop_oldest` | Ring buffer — drop oldest undelivered change |
| `error` | Mark pipeline as ERROR when full |

## Error policies

When a pipeline fails persistently:

| Policy | Behavior |
|---|---|
| `isolate` | Mark pipeline ERROR, continue all others |
| `stop_source` | Stop all pipelines on this source |
| `stop_all` | Halt the entire engine |

Failed changes can optionally be written to a dead letter store for later inspection and replay.
