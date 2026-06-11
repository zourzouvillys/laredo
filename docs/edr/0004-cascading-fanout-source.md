---
id: 4
title: "Cascading replication: a fan-out as a SyncSource (source/fanout)"
status: proposed
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

Today a laredo engine can **serve** a fan-out (the `target/fanout` target) and a
Go client can **consume** one (`client/fanout`). What is missing is letting a
*downstream engine* treat an *upstream fan-out* as a **source** — so fan-outs can
be **chained**: a regional or edge engine subscribes to an upstream fan-out and
re-serves its own fan-out, in-memory targets, or HTTP sync to local consumers,
without each leaf holding a PostgreSQL replication slot.

We add `source/fanout`: a `SyncSource` that wraps `client/fanout`. `Baseline`
yields the client's loaded rows; `Stream` forwards the client's live changes as
`ChangeEvent`s; resume and cross-instance failover come from the client
unchanged. The one real decision is the **position model** (below).

## Context

The fan-out protocol is a near-perfect source already:

- `client/fanout` does snapshot → delta/cold-replay resume → live, **plus**
  cross-instance failover by source position and local-snapshot fast restart.
  `source/fanout` should reuse it, not re-implement the protocol.
- It already exposes what a source needs: `AwaitReady`, `All()` (the loaded
  rows), `Listen(old,new)` (live changes), and `LastSourcePosition()`.

Two gaps shape the design:

1. **Schema discovery.** `SyncSource.Init` must return each table's columns. The
   `Sync` handshake carries `columns`, but `client/fanout` currently discards
   them. `source/fanout` needs them, so the client must expose the handshake
   column definitions (a small additive change to `client/fanout`).
2. **Positions.** The fan-out's journal entries carry the **upstream's** opaque
   `source_position` (e.g. a PostgreSQL WAL LSN). `source/fanout`'s positions are
   those strings — they are what makes resume stable across upstream instances.
   But `SyncSource.ComparePositions` must *order* them, and `source/fanout`
   cannot know the upstream source's position semantics. This is the crux.

## Decision

`source/fanout` implements `SyncSource` over a `client/fanout.Client`:

| SyncSource | Implementation |
|---|---|
| `Init` | dial + `AwaitReady`; return the handshake column definitions (newly exposed by the client). |
| `Baseline` | replay `client.All()` through `rowCallback`; return `client.LastSourcePosition()`. |
| `Stream(from, handler)` | resume the client from `from`; a `Listen` callback turns each `(old,new)` into a `ChangeEvent` (insert/update/delete/truncate) stamped with the current source position; block until ctx is done. |
| `Ack` / `LastAckedPosition` / `SupportsResume` | track the last applied source position; resume sends it as `last_known_source_position`. `SupportsResume() = true`. |
| `Pause` / `Resume` / `Close` | stop/start the client. |
| `OrderingGuarantee` | configurable; defaults to **`TotalOrder`** (a PostgreSQL-backed fan-out delivers a total order). |
| `Position{To,From}String` | identity (positions are already strings). |
| `ComparePositions` | **the pluggable comparator** — see below. |

### The position comparator

`source/fanout` takes a position **comparator** (`func(a, b string) int`):

- **Default: PostgreSQL LSN order** (`source/pg`'s LSN parse-and-compare). This is
  the dominant upstream — a fan-out almost always fronts a PostgreSQL source — so
  cascading works out of the box.
- **Override** (`WithPositionComparator`) for a non-PostgreSQL upstream, so the
  package stays source-agnostic. A misordered comparator only affects the
  engine's own bookkeeping (dedup on resume / ordering checks); delivery order
  itself comes from the upstream fan-out, which is already correct.

This keeps the common case zero-config and the general case expressible, without
`source/fanout` having to understand every possible upstream position format.

### Reuse, not reimplementation

`source/fanout` is a thin adapter: the connection, the three-phase catch-up,
resume, cold-tier replay (EDR-0002, server-side), and failover (GoAway handoff)
all come from `client/fanout` unchanged. The only library change outside the new
package is exposing the handshake columns on the client.

## Scope — in

- `source/fanout` implementing `SyncSource` over `client/fanout`.
- Expose the handshake `columns` on `client/fanout` (additive).
- The position comparator (default pg-LSN, `WithPositionComparator` override).
- An integration test: an upstream engine (testsource → `target/fanout` served
  over gRPC) consumed by a downstream engine (`source/fanout` → an in-memory
  target), asserting baseline + live changes propagate across the cascade, and
  resume after a downstream restart.
- Docs (a cascading guide / fan-out guide section), changelog.

## Scope — out

- **Bidirectional / multi-master.** This is one-directional cascade only. Merge
  semantics (e.g. binnacle's onboard reconciliation) stay in the application.
- **Fan-out-of-a-fan-out topology management.** `source/fanout` is the building
  block; orchestrating trees (discovery, health, loop prevention across many
  hops) is the operator's / a higher layer's concern.
- **A new wire protocol.** Cascading reuses the existing replication `Sync`.

## Consequences

**Easier:**

- **Edge / regional replicas.** A downstream engine re-serves an upstream
  fan-out locally — one upstream slot fans out to regions, each region fans out
  to its own leaves. Onboard nodes (binnacle) consume upstream this way.
- **Composability.** A cascaded source is just another `SyncSource`, so every
  target (in-memory, HTTP sync, another fan-out, the snapshotter) works
  downstream unchanged.
- **Inherited robustness.** Resume, cold-tier replay, and failover come from the
  client for free.

**Harder / obligations:**

- **Position semantics travel.** The downstream orders by the upstream's position
  format; the default fits PostgreSQL, others must supply a comparator. Documented.
- **Lag compounds across hops.** Each cascade adds a hop of propagation delay and
  a re-materialized in-memory copy. `GetLag` surfaces it per hop; operators size
  chains accordingly.
- **Loops are the operator's responsibility.** Nothing stops A→B→A if mis-wired;
  topology hygiene is out of scope for the building block.

## References

- [Fan-out guide](/guides/fan-out) — the protocol and `client/fanout`
- `client/fanout` — the client this wraps (snapshot + resume + failover)
- `source.go` — the `SyncSource` contract
- `source/pg/lsn.go` — the default LSN comparator

## Changelog

- **2026-06-11**: Proposed.
