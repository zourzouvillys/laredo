---
sidebar_position: 6
title: Ordering & Delivery
---

# Ordering & Delivery Guarantees

## Delivery semantics

Laredo provides **at-least-once** delivery. The engine defers ACK until all targets confirm durability. On restart:

- **Ephemeral sources**: full re-baseline (no resume capability)
- **Stateful sources**: resume from last ACKed position, which may redeliver some changes

Targets must be idempotent for the overlap window after a restart.

## Ordering by source type

| Source | Guarantee | Implication |
|---|---|---|
| PostgreSQL | **Total order** | All changes across all tables in commit order |
| S3 + Kinesis | **Per-partition order** | Ordered within a shard, no cross-shard guarantee |

## Baseline-to-stream consistency

There is no gap between the baseline snapshot and the change stream. PostgreSQL's exported snapshot mechanism ensures the baseline represents a consistent point-in-time, and the change stream picks up from exactly that point.

## ACK coordination

When a table fans out to multiple targets, the engine ACKs the source position only after **all** targets have confirmed `IsDurable()`. The ACK position is the minimum confirmed position across all pipelines sharing that source.

```
Source position: 100
  Pipeline A (indexed-memory): durable at 100  ✓
  Pipeline B (http-sync):      durable at 97   ✓ (batch not yet flushed)
  Pipeline C (http-sync):      durable at 100  ✓

ACK position = min(100, 97, 100) = 97
```

The source retains data from position 97 onward. If the process crashes, changes 98-100 will be redelivered to pipeline B on restart.

## Schema changes

When a source detects a column schema change, it notifies each target via `OnSchemaChange`. Targets respond with one of:

| Response | Meaning |
|---|---|
| `CONTINUE` | Target adapted, keep streaming |
| `RE_BASELINE` | Target needs a full reload |
| `ERROR` | Target can't handle this change |
