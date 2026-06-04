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

<svg class="diagram" viewBox="0 0 720 200" role="img" aria-label="One PostgreSQL slot is demuxed by table and fanned out to three pipelines for the same table, each ending in a different target.">
  <!-- source -->
  <rect x="8" y="76" width="120" height="48" rx="10" fill="var(--bg-subtle)" stroke="var(--allow)" stroke-width="1.5"/>
  <text x="68" y="98" text-anchor="middle" fill="var(--allow)" font-size="12" font-weight="700">PostgreSQL</text>
  <text x="68" y="114" text-anchor="middle" fill="var(--fg-muted)" font-size="10">1 slot</text>
  <!-- engine demux -->
  <rect x="220" y="72" width="120" height="56" rx="10" fill="var(--bg-subtle)" stroke="var(--accent)" stroke-width="2"/>
  <text x="280" y="96" text-anchor="middle" fill="var(--accent)" font-size="12" font-weight="700">engine</text>
  <text x="280" y="112" text-anchor="middle" fill="var(--fg-muted)" font-size="10">demux by table</text>
  <line x1="128" y1="100" x2="220" y2="100" stroke="var(--border)" stroke-width="1.5"/>
  <!-- three pipelines for the same table → three targets -->
  <g stroke="var(--border)" stroke-width="1.5" fill="none">
    <path d="M340 100 C 400 100, 400 40, 470 40"/>
    <path d="M340 100 L 470 100"/>
    <path d="M340 100 C 400 100, 400 160, 470 160"/>
  </g>
  <g font-size="11.5">
    <rect x="470" y="22" width="240" height="36" rx="8" fill="var(--main-soft)" stroke="var(--main)" stroke-width="1.2"/>
    <text x="484" y="44" fill="var(--fg)">config_document → <tspan font-weight="700" fill="var(--main)">indexed-memory</tspan></text>
    <rect x="470" y="82" width="240" height="36" rx="8" fill="var(--bg-muted)" stroke="var(--border)" stroke-width="1.2"/>
    <text x="484" y="104" fill="var(--fg)">config_document → <tspan font-weight="700" fill="var(--fg-secondary)">http-sync</tspan></text>
    <rect x="470" y="142" width="240" height="36" rx="8" fill="var(--canary-soft)" stroke="var(--canary)" stroke-width="1.2"/>
    <text x="484" y="164" fill="var(--fg)">config_document → <tspan font-weight="700" fill="var(--canary)">replication-fanout</tspan></text>
  </g>
</svg>

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
