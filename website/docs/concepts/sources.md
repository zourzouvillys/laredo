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

<svg class="diagram" viewBox="0 0 720 250" role="img" aria-label="Source state machine: connecting → connected → streaming; connected or streaming drop to reconnecting on connection loss; reconnecting retries back to connecting or, after max retries, goes to error.">
  <defs>
    <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--fg-muted)"/>
    </marker>
  </defs>
  <style>
    .n rect { stroke-width: 1.8; }
    .n text { font-size: 12px; font-weight: 700; }
    .e { stroke: var(--fg-muted); stroke-width: 1.6; fill: none; marker-end: url(#arrow); }
    .lbl { font-size: 10.5px; fill: var(--fg-muted); }
  </style>
  <!-- edges -->
  <path class="e" d="M156 56 L238 56"/>
  <text class="lbl" x="197" y="46" text-anchor="middle">connect</text>
  <path class="e" d="M362 56 L443 56"/>
  <text class="lbl" x="402" y="46" text-anchor="middle">baseline done</text>
  <path class="e" d="M300 73 C 300 120, 330 130, 348 148"/>
  <text class="lbl" x="300" y="118" text-anchor="end">conn. lost</text>
  <path class="e" d="M500 73 C 470 120, 430 132, 402 148"/>
  <text class="lbl" x="470" y="120" text-anchor="middle">conn. lost</text>
  <path class="e" d="M286 168 C 180 150, 120 120, 110 82"/>
  <text class="lbl" x="170" y="120" text-anchor="middle">retry ↺</text>
  <path class="e" d="M286 176 L176 176"/>
  <text class="lbl" x="231" y="192" text-anchor="middle">max retries</text>
  <!-- nodes -->
  <g class="n">
    <g><rect x="35" y="39" width="120" height="34" rx="8" fill="var(--bg-subtle)" stroke="var(--accent)"/><text x="95" y="61" text-anchor="middle" fill="var(--accent)">CONNECTING</text></g>
    <g><rect x="240" y="39" width="120" height="34" rx="8" fill="var(--bg-subtle)" stroke="var(--accent)"/><text x="300" y="61" text-anchor="middle" fill="var(--accent)">CONNECTED</text></g>
    <g><rect x="445" y="39" width="120" height="34" rx="8" fill="var(--main-soft)" stroke="var(--main)"/><text x="505" y="61" text-anchor="middle" fill="var(--main)">STREAMING</text></g>
    <g><rect x="288" y="149" width="130" height="34" rx="8" fill="var(--queue-soft)" stroke="var(--queue)"/><text x="353" y="171" text-anchor="middle" fill="var(--queue)">RECONNECTING</text></g>
    <g><rect x="56" y="159" width="120" height="34" rx="8" fill="var(--reject-soft)" stroke="var(--reject)"/><text x="116" y="181" text-anchor="middle" fill="var(--reject)">ERROR</text></g>
  </g>
</svg>

## Ephemeral vs. stateful

Whether a source can resume from a previously ACKed position is a property of the source configuration:

- **Ephemeral**: every startup requires a full baseline. Simple, no state to manage.
- **Stateful**: resume from the last ACKed position. No data reprocessing on restart, but requires persistent position tracking (e.g., a named PostgreSQL replication slot).

Resuming only reconstructs a target whose state **survives the restart** (e.g. an external database). A non-durable target — an in-memory target rebuilt empty on each start — would come up empty and only see post-restart changes. For those, use ephemeral mode, [snapshots](./snapshots.md), or the PostgreSQL source's [`always_baseline`](../guides/postgresql.md#always-baseline-non-durable-targets) option to force a full COPY on every startup.
