---
id: 6
title: "Archive as a SyncSource: replay a snapshotter archive without a database"
status: accepted
date: 2026-07-20
authors:
  - "Theo Zourzouvillys <theo@zrz.io>"
tags: [source, snapshot, archive, offline, seed, backup, laredo-server, cli]
supersedes: null
superseded_by: null
aliases: []
---

## TL;DR

Consumers of laredo need a live PostgreSQL (or Kinesis/S3) to feed the engine.
There is no way to run the engine from a **static file**. That blocks offline
backup/restore, immediate startup with no database, and seeding local development.

The snapshotter ([EDR-0001](/edr/0001-snapshot-writer)) already writes a
versioned, binary archive — a base snapshot plus a chain of diffs indexed by a
per-table manifest — with a read-side `snapshotter.Reader` that folds it offline
([EDR-0003](/edr/0003-point-in-time-reconstruction)), a local destination, and
config plumbing to build a reader from HOCON ([EDR-0005](/edr/0005-archive-from-hocon)).
Cold-tier replay ([EDR-0002](/edr/0002-cold-tier-replay)) already consumes that
archive to feed live replication clients.

This EDR adds the missing direction: a `source/archive` **`SyncSource`** that
replays that archive as the engine's input, plus a `laredo archive export`
producer and cross-restart resume. No new file format; the source is a thin
adapter over `snapshotter.Reader`.

## Context

Everything needed to *read* an archive exists; nothing presents it to the engine
as a source. The forces shaping the design:

- **The archive is per `(schema, table)`.** A snapshotter manifest is per-table,
  and `source/fanout` ([EDR-0004](/edr/0004-cascading-fanout-source)) is already a
  complete non-PostgreSQL `SyncSource` with opaque **string** positions and a
  pluggable comparator. So one archive source serves one table, positions are
  strings, and the fan-out source is the template — not the composite-position
  Kinesis source.
- **`key_prefix` is a contract with the writer.** The reader's keys derive from
  the prefix the archive was written under; there is no safe default that always
  matches, so it is operator-supplied. Same coupling EDR-0005 documents for the
  fan-out target's archive block.
- **The reader-building config already exists.** `config.BuildArchiveReader` +
  `snapshotter/destwire` (EDR-0005) turn `store`/`store_config`/`format`/
  `key_prefix` into a `*snapshotter.Reader`. The source reuses it; `config`
  already imports source packages, so the source must **not** import `config`
  (it takes a pre-built reader, exactly as EDR-0005 hands readers to
  `replication.WithArchive`).
- **The manifest records the table, not its columns.** An offline source cannot
  ask a live catalog for the schema, so schema fidelity needs the archive to
  carry it.

## Decision

### 1. `source/archive` — a `SyncSource` over `snapshotter.Reader`

Modeled on `source/fanout`. `New(WithReader(r), Table(schema, table), …)`.
Positions are strings ordered by a pluggable comparator (default WAL-LSN, shared
via the new `internal/lsn` package). `Baseline` reconstructs the table's current
state at the archive head and emits it; `Stream` replays diffs recorded after the
head as change events.

- **Follow** (`follow = true`) polls the manifest for appended diffs.
- **Wholesale replacement** — a fresh base whose position no longer continues the
  consumer's — returns the existing `laredo.ErrReBaselineRequired` sentinel, and
  the engine re-baselines. Append = emit diffs; replace = re-baseline, with no new
  engine machinery. This is the same path PostgreSQL uses on an invalid slot.
- **Resume** — with a `state_path`, `Ack` persists the position (atomic write) and
  `LastAckedPosition` reads it back, so a restart continues from the last ACK.
  Without it, the source re-baselines every start (safe for non-durable targets).

### 2. `laredo archive export` — one-shot and continuous producers

**One-shot.** `snapshotter.WriteBaseSnapshot` writes a single base-snapshot
archive (one snapshot artifact plus a fresh manifest **recording the schema**), a
point-in-time dump with no prior manifest to reconcile. Re-export overwrites the
manifest, which a `follow` source reads as a replacement.

**Continuous** (`--follow`). The `snapshotter/sourcesub` adapter presents any
`laredo.SyncSource` as a `snapshotter.Subscription` — maintaining the source's
current state in memory (baseline plus applied changes) so the Writer can
re-snapshot on demand, and re-baselining when the source asks (e.g. a PostgreSQL
reconnect). The existing `Writer` then materializes the source into a live
base-plus-diffs archive, so PostgreSQL can be archived directly without running a
fan-out. A new optional `snapshotter.SchemaProvider` interface (which the adapter
implements) lets the Writer record the schema at commit time — non-breaking for
`fanoutsub`, which simply omits it.

Both forms connect directly, in keeping with the offline-first archive command
family (EDR-0003).

### 3. Schema fidelity — an additive manifest field

The `Manifest` gains an optional `Columns []laredo.ColumnDefinition`, written by
export and read by the source's `Init`. It is additive and forward-compatible —
older readers ignore it, older archives omit it — so **the manifest version is not
bumped** (version bumps are for breaking the contract, not extending it). When
columns are absent the source infers names from a snapshot row, types unset.

### 4. Config wiring reuses EDR-0005

The config-layer `SourceConfig` gains the archive fields (reusing `ArchiveConfig`
for store/format/key_prefix), a `case "archive"/"file"` in `createSource` builds
the reader via `BuildArchiveReader` and constructs the source, and `Validate`
surfaces store/format errors at load time. The served table is derived from the
pipeline that binds the source, so it is not repeated in the source block.

## Scope — in

- `source/archive`: a `SyncSource` replaying a snapshotter archive, with follow,
  wholesale-replacement re-baseline, and state-file resume.
- `snapshotter.WriteBaseSnapshot` and `laredo archive export` (PostgreSQL → local
  or s3 archive), plus the optional manifest `Columns`.
- Config: `type = archive | file` parsed, validated, and wired through the reused
  EDR-0005 destination/format machinery.
- The shared `internal/lsn` comparator, extracted from `source/fanout` and
  `cmd/laredo` (removing the duplicates rather than adding a third).
- Tests: source replay/follow/replace/resume/schema, export round-trip, and config
  parse/build end-to-end. Docs: the archive-source guide, sources/config/CLI
  reference, and this EDR.

## Scope — out

- **A server-side export RPC.** Export stays a CLI/offline operation, consistent
  with EDR-0003; no new OAM surface.
- **Named profiles / assume-role for the export/source S3 path.** Ambient AWS
  credentials only, matching EDR-0005.

## Consequences

**Easier:**

- **An engine starts with no database** — from an offline backup, or a committed
  dev seed — configured, not coded.
- **One recognisable schema.** Operators who run the snapshotter or a fan-out
  archive already know `store`/`store_config`/`format`/`key_prefix`.
- **Schema round-trips losslessly** now that the manifest can carry columns —
  benefiting cold replay and reconstruction too, not just this source.

**Harder:**

- **`key_prefix` is load-bearing config.** A mismatch surfaces as "manifest not
  found" at startup; the docs call this out plainly.
- **A follow source re-baselines on re-base.** Following a *live* archive that
  re-bases frequently re-folds the head each time — correct but heavier. Static
  seeds (the common case) never trigger it.

**New obligations:**

- **The manifest `Columns` field is part of the archive contract.** It stays
  optional and additive; a future breaking schema change is what bumps the
  manifest version.

## References

- [EDR-0001 — Snapshot writer](/edr/0001-snapshot-writer) — the archive layout and format.
- [EDR-0002 — Cold-tier replay](/edr/0002-cold-tier-replay) — the other archive consumer; the `ErrReBaselineRequired` handoff pattern.
- [EDR-0003 — Point-in-time reconstruction](/edr/0003-point-in-time-reconstruction) — offline reads and the CLI family this export joins.
- [EDR-0004 — Cascading fan-out source](/edr/0004-cascading-fanout-source) — `source/fanout`, the `SyncSource` template.
- [EDR-0005 — Cold-tier archive from HOCON](/edr/0005-archive-from-hocon) — `BuildArchiveReader` / `destwire`, reused here.
- [Archive source guide](/guides/archive-source) — usage, operations, troubleshooting.

## Changelog

- **2026-07-20**: Proposed and accepted; implemented. `source/archive` replays a
  snapshotter archive as a `SyncSource` (follow + wholesale-replacement
  re-baseline + state-file resume); `laredo archive export` and
  `snapshotter.WriteBaseSnapshot` produce one-shot archives; the manifest carries
  an optional schema; config wires `type = archive | file` through the EDR-0005
  reader machinery; `internal/lsn` consolidates the WAL-LSN comparator.
- **2026-07-28**: Continuous export shipped. `snapshotter/sourcesub` adapts any
  `SyncSource` to a `snapshotter.Subscription`, and `laredo archive export
  --follow` drives the Writer with it — a live base-plus-diffs archive sourced
  straight from PostgreSQL. An optional `snapshotter.SchemaProvider` lets the
  Writer record the schema (non-breaking for the fan-out subscription).
- **2026-07-28**: Multi-table groups and re-baseline observability shipped.
  `group = true` on an archive source block expands, in `ToEngineOptions`, to one
  single-table source per referencing table (derived `key_prefix` and per-table
  state file), so one block covers many tables — no engine change. A new
  `EngineObserver.OnReBaselineTriggered(sourceID)` hook, fired where the engine
  handles `ErrReBaselineRequired`, surfaces `laredo_source_rebaseline_total` /
  `laredo.source.rebaseline` in the Prometheus and OTel observers — making
  re-baselines (archive replacement, PostgreSQL reconnect) observable for all
  sources.
