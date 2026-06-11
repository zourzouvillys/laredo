---
id: 4
title: "Cascading replication: a fan-out as a SyncSource (source/fanout)"
status: accepted
date: 2026-06-11
authors:
  - "Theo Zourzouvillys <theo@zrz.io>"
tags: [fan-out, source, cascading, replication, edge, multi-region]
supersedes: null
superseded_by: null
aliases: []
proposed_until: 2026-09-11
---

## TL;DR

Today a laredo engine can **serve** a fan-out (`target/fanout`) and a Go client
can **consume** one (`client/fanout`). What was missing is letting a *downstream
engine* treat an *upstream fan-out* as a **source** — so fan-outs can be
**chained**: a regional or edge engine subscribes to an upstream fan-out and
re-serves its own fan-out, in-memory targets, or HTTP sync to local consumers,
without each leaf holding a PostgreSQL replication slot.

We add `source/fanout`: a `SyncSource` that wraps `client/fanout`. `Baseline`
yields the client's loaded rows; `Stream` forwards the client's live changes as
`ChangeEvent`s; resume, cold-tier replay, and cross-instance failover come from
the client unchanged. The one real decision is the **position model** (below).

## Context

The fan-out protocol is a near-perfect source already:

- `client/fanout` does snapshot → delta / cold-replay resume → live, **plus**
  cross-instance failover by source position and local-snapshot fast restart.
  `source/fanout` reuses it rather than re-implementing the protocol.
- It already exposes most of what a source needs: `AwaitReady`, `All()` (the
  loaded rows), and `LastSourcePosition()`.

Two gaps shaped the design, both closed by small additive changes:

1. **Schema discovery.** `SyncSource.Init` must return each table's columns. The
   `Sync` handshake carries `columns`, but the replication service did not
   populate them and `client/fanout` discarded them. Now the service sends the
   fan-out target's columns in the handshake, and `client/fanout` exposes them
   via `Columns()`.
2. **Per-change positions.** A source stamps each `ChangeEvent` with a position,
   but `client/fanout`'s `Listen(old,new)` callback carries no position, and
   reading `LastSourcePosition()` inside the callback would deadlock (the client
   holds its lock while notifying). We add `ListenWithPosition(old,new,position)`
   — the client passes the change's source position, which it already has under
   that lock.

## Decision

`source/fanout` implements `SyncSource` over a `client/fanout.Client`:

| SyncSource | Implementation |
|---|---|
| `Init` | dial + `AwaitReady`; register `ListenWithPosition` (before awaiting ready, so no change between baseline and stream is missed); return `client.Columns()`. |
| `Baseline` | replay `client.All()` through `rowCallback`; return `client.LastSourcePosition()`. |
| `Stream(from, handler)` | forward buffered + live changes whose position is `> from` (those at or before `from` are already in the baseline) as `ChangeEvent`s; block until ctx is done. |
| `Ack` / `LastAckedPosition` / `SupportsResume` | track the last forwarded source position; the client resumes by sending it as `last_known_source_position`. `SupportsResume() = true`. |
| `OrderingGuarantee` | configurable; defaults to **`TotalOrder`** (a PostgreSQL-backed fan-out is totally ordered). |
| `Position{To,From}String` | identity (positions are already strings). |
| `ComparePositions` | **the pluggable comparator** — see below. |

The change listener pushes each change onto an internal queue with a quick mutex
append (it runs under the client's lock, so it must not block); `Stream` drains
the queue and forwards. This buffering is what closes the baseline→stream gap.

### The position comparator

`source/fanout` takes a position **comparator** (`func(a, b string) int`):

- **Default: PostgreSQL LSN order.** A fan-out almost always fronts a PostgreSQL
  source, so cascading works out of the box. The empty string (a fresh
  snapshot's reset position) sorts lowest.
- **Override** (`WithPositionComparator`) for a non-PostgreSQL upstream, so the
  package stays source-agnostic. A misordered comparator only affects the
  engine's own bookkeeping (dedup on resume / ordering checks); delivery order
  itself comes from the upstream fan-out, which is already correct.

## Scope — in

- `source/fanout` implementing `SyncSource` over `client/fanout`.
- `client/fanout.Columns()` and `ListenWithPosition` (additive); the replication
  handshake now carries the fan-out target's columns.
- The position comparator (default pg-LSN, `WithPositionComparator` override).
- An integration test cascading two engines (upstream fan-out → `source/fanout` →
  in-memory target), asserting baseline + live propagation; docs, changelog.

## Scope — out

- **Bidirectional / multi-master.** One-directional cascade only; merge semantics
  (e.g. binnacle's onboard reconciliation) stay in the application.
- **Topology management.** `source/fanout` is the building block; orchestrating
  trees (discovery, health, **loop prevention** across hops) is a higher layer's
  concern.
- **True pause.** `Pause`/`Resume` are best-effort state flips; the client keeps
  its connection. A future revision may stop the upstream stream while paused.
- **A new wire protocol.** Cascading reuses the existing replication `Sync`.

## Consequences

**Easier:**

- **Edge / regional replicas.** A downstream engine re-serves an upstream fan-out
  locally — one upstream slot fans out to regions, each region to its own leaves.
- **Composability.** A cascaded source is just another `SyncSource`, so every
  target (in-memory, HTTP sync, another fan-out, the snapshotter) works
  downstream unchanged.
- **Inherited robustness.** Resume, cold-tier replay, and failover come from the
  client for free.

**Harder / obligations:**

- **Position semantics travel.** The downstream orders by the upstream's position
  format; the default fits PostgreSQL, others must supply a comparator. Documented.
- **Lag compounds across hops.** Each cascade adds a hop of delay and a
  re-materialized in-memory copy. Operators size chains accordingly.
- **Loops are the operator's responsibility.** Nothing stops A→B→A if mis-wired.

## References

- [Fan-out guide](/guides/fan-out) — the protocol and `client/fanout`
- `client/fanout` — the client this wraps (snapshot + resume + cold replay + failover)
- `source.go` — the `SyncSource` contract
- `source/pg/lsn.go` — the LSN format the default comparator mirrors

## Changelog

- **2026-06-11**: Proposed.
- **2026-06-11**: Accepted; implemented — `source/fanout`, `client/fanout`
  `Columns()` + `ListenWithPosition`, and handshake column propagation.
