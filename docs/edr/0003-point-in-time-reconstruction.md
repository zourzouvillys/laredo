---
id: 3
title: "Point-in-time reconstruction (as-of reads) from the archive"
status: accepted
date: 2026-06-11
authors:
  - "Theo Zourzouvillys <theo@zrz.io>"
tags: [snapshot, diff, archive, reconstruction, as-of, time-travel]
supersedes: null
superseded_by: null
aliases: []
proposed_until: 2026-09-11
---

## TL;DR

The snapshotter archive ([EDR-0001](/edr/0001-snapshot-writer)) is a base
snapshot plus a stream of diffs, each watermarked by source position. Cold-tier
replay ([EDR-0002](/edr/0002-cold-tier-replay)) added `snapshotter.Reader` to walk
that archive *forward* from a position. The same machinery answers a second,
generic question for free: **"what did the table look like as of position P?"**

We add `Reader.ReconstructAsOf(ctx, position, keyFields, cmp)` — it selects the
newest base snapshot at or before P, folds the diffs up to P into a keyed map,
and returns the materialized rows plus the **effective position** the result
reflects. This is point-in-time/time-travel reads for audit, debugging, and
offline point-in-time exports, built entirely from artifacts already written.

## Context

EDR-0001 scoped reconstruction out of the *binary* ("the consumer rebuilds")
and EDR-0002 gave consumers `Reader` for the forward (resume) case, explicitly
naming as-of reads as "its own EDR." The pieces are all present:

- Artifacts carry positions: a snapshot at `ToPosition`, a diff over
  `(FromPosition, ToPosition]`.
- `Reader` already loads the manifest and decodes snapshots/diffs.
- Diffs are **not splittable** (they are PK-collapsed over a range), so a read
  "as of P" resolves to the state at the latest artifact boundary **at or
  before** P — the *effective* position. That is the natural, honest semantics
  and needs no per-change positions.

## Decision

Two additions to `snapshotter.Reader`, plus one shared helper:

- **`PlanAsOf(manifest, position, cmp) (*ReplayPlan, error)`** — the newest base
  snapshot with `ToPosition ≤ position`, plus the contiguous diffs after it whose
  `ToPosition ≤ position`. A diff that straddles `position` is excluded. Returns
  `nil` when the archive cannot cover `position` (empty manifest, or the oldest
  snapshot is already after `position`).
- **`ReconstructAsOf(ctx, position, keyFields, cmp) (*Reconstruction, error)`** —
  loads the manifest, plans, reads the base snapshot and diffs, and **folds**
  them into a keyed map (insert/update set, delete removes, truncate clears),
  returning `{Rows, Position}` where `Position` is the effective as-of position.
  Returns `nil` when the archive cannot reach `position`; the caller decides
  whether that is an error.
- **`RowKey(row, keyFields)`** — lifted from the writer's private `keyOf`, now
  shared so a reconstructed snapshot keys rows exactly as the diff stream does
  (no drift between write and read). `keyFields` defaults to `["id"]`.

It stays a **library** capability (EDR-0001's "reconstruction is the consumer's
job"): the snapshotter binary remains write-only, and the replication service is
untouched. `Reader` reuses its existing `Destination` / `Format` decode paths —
no new artifact format, storage backend, or wire change.

## Scope — in

- `PlanAsOf`, `ReconstructAsOf`, `Reconstruction`, and the shared `RowKey`.
- Unit tests over a real local destination (boundary, between-boundary,
  predates-archive, delete/insert/update folding, effective-position).
- Docs (snapshot-writer guide reconstruction section), changelog.

## Scope — out

- **No CLI / RPC surface.** A `laredo` CLI command or a read RPC over the archive
  is a natural follow-up but not required for the library capability. Noted in
  `TODO.md`.
- **No sub-diff (intra-range) precision.** As-of resolves to artifact boundaries;
  splitting a diff to land exactly on a non-boundary position is out of scope
  (and would need per-change positions the archive does not record).
- **No new metric/threshold.** Read-only over existing artifacts.

## Consequences

**Easier:**

- **Audit & debugging.** "What did this table look like at LSN X / time T" is a
  direct call, from cheap object storage, with no live system involved.
- **Point-in-time exports.** A consumer can materialize a consistent historical
  snapshot for a data lake or a diff against "now".
- **Reuse.** Built on the `Reader` and `Format` paths cold-tier replay already
  uses; `RowKey` removes the last bit of write/read key-derivation duplication.

**Harder / obligations:**

- **`keyFields` must match the write side.** Reconstruction keys snapshot rows
  by the same fields the snapshotter wrote with; a mismatch silently mis-keys.
  Documented; the default `["id"]` matches the writer default.
- **Effective-vs-requested position.** Callers must use the returned
  `Position`, not assume the result is exactly at the requested position.
  Documented on the type.

## References

- [EDR-0001 — snapshot + diff artifacts](/edr/0001-snapshot-writer)
- [EDR-0002 — cold-tier replay](/edr/0002-cold-tier-replay) — introduced `Reader`
- `snapshotter/reader.go` — `Reader`, `Plan`, `PlanAsOf`, `ReconstructAsOf`

## Changelog

- **2026-06-11**: Accepted; implemented alongside this EDR.
