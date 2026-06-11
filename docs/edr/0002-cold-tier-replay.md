---
id: 2
title: "Cold-tier replay: resume stale fan-out clients from the snapshotter archive"
status: proposed
date: 2026-06-10
authors:
  - "Theo Zourzouvillys <theo@zrz.io>"
tags: [fan-out, replication, snapshot, diff, archive, s3, replay]
supersedes: null
superseded_by: null
aliases: []
proposed_until: 2026-09-10
---

## TL;DR

When a fan-out client's resume position predates the in-memory change journal,
the server today can only send it a **full snapshot of live state** — the delta
optimization is lost, and the client re-downloads the whole table even though it
only fell slightly behind the (bounded) journal.

We close that gap by teaching the replication service to **replay from the cold
archive** that [`laredo-snapshotter`](/edr/0001-snapshot-writer) already writes.
EDR-0001 deliberately left reconstruction out of the *binary*; this EDR adds the
**read side** as a reusable library (`snapshotter.Reader`) and wires it into the
fan-out's `Sync` path as a new mode, `SYNC_MODE_REPLAY_ARCHIVE`. A too-stale
client is served the newest base snapshot and the diffs after it (or, when a
contiguous diff chain reaches back far enough, *only* the diffs), streamed
through the existing wire messages, and then handed off to the hot journal and
the live tail — gaplessly, by source position.

The result: a consumer that has been offline far longer than the hot journal
retains — an onboard node at sea for a week, a regional replica catching up,
a service restarting after a long outage — resumes with **history from cold
storage** instead of a full live re-snapshot, and the snapshotter's archive
gains its first first-class read consumer. Correctness always falls back to the
existing live full-snapshot path when the archive cannot bridge the gap.

## Context

Two facts about today's fan-out set up the problem:

- The fan-out target's change journal is a **bounded circular buffer** (pruned by
  `max_entries` / `max_age`). A client whose `last_known_source_position`
  predates `journal_oldest` cannot be served a delta; the server falls back to
  `SYNC_MODE_FULL_SNAPSHOT` of the **live in-memory state**
  ([fan-out guide](/guides/fan-out)). That is correct but expensive, and it
  discards the history the client missed.
- `laredo-snapshotter` ([EDR-0001](/edr/0001-snapshot-writer)) already
  subscribes to the fan-out and writes the table to object storage as a **base
  snapshot + a stream of diffs**, each watermarked by source position (WAL LSN)
  and indexed by a CAS-written **manifest**. But EDR-0001 scoped *reconstruction*
  out of the binary: *"Consumers rebuild from artifacts; this binary never reads
  its own output back to answer queries."*

So the durable, position-watermarked history a stale client needs **already
exists** — it just has no reader on the replication path. Building an independent
journal-segment archive inside the fan-out target would duplicate that history
into a second object-storage layout, doubling what operators must run and
reconcile. The missing piece is not more archiving; it is the **read side** of
the archive we already write.

The forces that shape the design:

- The archive and the hot journal are produced by **two independently-advancing
  systems** (the snapshotter is itself a fan-out client; the journal is the
  source-of-truth buffer). The handoff between "cold archive head" and "hot
  journal tail" must be **gapless** — the same atomic-handoff concern as
  snapshot→journal, but now spanning two systems that share only the **source
  position** as a common coordinate.
- The archive lives behind object-storage latency. Cold catch-up is necessarily
  slower than a hot delta; that is acceptable because it is the *stale* path, but
  it must not stall the live path or hold locks that block other clients.
- The snapshotter's manifest is a **compatibility contract** (EDR-0001). Reading
  it couples the replication service to that contract; the coupling must be
  versioned and fail safe (an unreadable or unknown-version manifest must fall
  back, never guess).
- The `Format` interface is **already bidirectional** (`ReadSnapshot`,
  `ReadDiff`) and the `Destination` interface already exposes `Get`. The read
  side reuses both — no new decoders, no new storage adapters.

## Decision

Add cold-tier replay in two layers.

### 1. `snapshotter.Reader` — the reconstruction library

The inverse of `Writer`, in the same package (the package owns the artifact
format, so it owns both directions). Given a `Destination`, the set of `Format`s
keyed by `FormatID`, and the table's `KeyPrefix`, it offers:

- `LoadManifest(ctx) (*Manifest, error)` — read and parse the manifest, checking
  `manifest_version`.
- `ReadSnapshot(ctx, art) ([]laredo.Row, error)` and
  `ReadDiff(ctx, art) ([]Change, error)` — fetch an artifact's bytes (in the
  first available configured format) and decode them via the matching `Format`.
- `Plan(manifest, fromPosition, cmp) (*ReplayPlan, error)` — given a resume
  position and a position comparator, select the artifact chain to send:
  - **Diff-only** when the manifest holds a *contiguous* diff chain whose ranges
    cover `(fromPosition, head]` with no gap: the plan is that ordered list of
    diffs. The client already has the state up to `fromPosition`; it only needs
    the changes after it.
  - **Snapshot-base** otherwise (the position predates the chain, or a re-base
    broke contiguity): the plan is the newest base snapshot plus every diff whose
    range begins at or after the snapshot's position. The client rebuilds from a
    clean base.
  - **None** when the manifest is empty, unreadable, an unknown version, or its
    `head` predates `fromPosition` with no covering chain — the caller falls back
    to the live path.

Reconstruction is exposed as a **library** so any consumer (a Spark loader, a
time-travel reader — EDR-TBD) can use it, honouring EDR-0001's "reconstruction is
the consumer's job" while finally giving consumers a first-class tool for it. To
support it, the artifact/manifest **object-key derivation** is lifted from
`Writer`'s private methods to exported package functions
(`ManifestObjectKey(prefix)`, `ArtifactObjectKey(prefix, art, ext)`) used by both
sides, so reader and writer never drift on layout.

### 2. `SYNC_MODE_REPLAY_ARCHIVE` — the replication path

A fan-out target may be configured with an optional **archive** it can read
(the same archive the snapshotter writes). When present, the `Sync` mode
selection gains one branch, evaluated only on the existing too-stale fallback:

```
position resume covered by hot journal?      → DELTA            (unchanged)
recent sequence / known snapshot?            → DELTA[_FROM_SNAPSHOT] (unchanged)
archive configured AND can bridge the gap?   → REPLAY_ARCHIVE   (new)
otherwise                                    → FULL_SNAPSHOT     (unchanged fallback)
```

"Can bridge the gap" means: the `Reader.Plan` returns a chain **and** the chain's
head position is at or beyond the hot journal's oldest retained position (so
cold→hot is contiguous). If the snapshotter has fallen so far behind that the hot
journal pruned past the archive head, there is a genuine gap; cold replay is
declined and the server falls back to `FULL_SNAPSHOT`. Cold replay is strictly an
optimization on the stale path — never a correctness dependency.

The stream a `REPLAY_ARCHIVE` client receives reuses the **existing wire
messages**, so current clients need **no changes** (they apply messages
regardless of handshake mode):

1. **Handshake** with `mode = SYNC_MODE_REPLAY_ARCHIVE` (for observability and so
   a client may surface "cold replay in progress" and expect object-storage
   latency).
2. **Pin the hot journal** at the archive head position for the duration, so it
   cannot prune past the handoff point mid-replay (mirrors the snapshot pin).
3. If the plan is snapshot-base: `SnapshotBegin` → `SnapshotRow…` (decoded base
   rows) → `SnapshotEnd`, all stamped at the base's position.
4. **Diff entries** → one `ReplicationJournalEntry` per `Change` (mapping
   `New→new_values`, `Old→old_values`, action as-is), each carrying its diff's
   `to_position` as `source_position`. Within a diff, changes are PK-collapsed,
   so order among them is immaterial; diffs are sent in position order.
5. **Hot-journal handoff** — replay hot journal entries with position strictly
   after the archive head, then transition to **live**, exactly as the existing
   catch-up does. Resume is **at-least-once** across the boundary; clients are
   idempotent by primary key, so a brief overlap is safe (the same guarantee the
   protocol already makes on reconnect/failover).

If anything on the cold path fails (manifest read error, missing artifact, decode
error, unknown manifest version), the handler logs it and **falls back to
`FULL_SNAPSHOT`** before any rows are sent — the client always gets a correct,
complete state.

### Configuration

A fan-out target gains an optional, read-only `archive` block naming the same
destination/prefix the snapshotter writes, and the formats it may decode:

```hocon
targets = [{
  type = replication-fanout
  # ...existing journal / snapshot / grpc / client_buffer blocks...

  # Optional: serve too-stale clients from the snapshotter's cold archive
  # instead of a full live re-snapshot.
  archive {
    destination = s3
    destination_config { bucket = "my-bucket", prefix = "laredo/public.events/" }
    formats = [jsonl]            # decoders to try, in order
  }
}]
```

Absent the block, behaviour is exactly as today (too-stale → live full snapshot).
The archive is **read-only** from the fan-out's perspective; the snapshotter
remains the sole writer.

### Pluggable seams (reused, not added)

- **`Destination`** — read via `Get`; no new storage backend.
- **`Format`** — decode via `ReadSnapshot` / `ReadDiff`; no new format.
- **Position comparison** — the replication handler already resolves the owning
  `SyncSource` and uses `PositionFromString` / `ComparePositions` for failover;
  cold replay reuses them to order and bound the archive chain against the hot
  journal.

## Scope — in

- `snapshotter.Reader`: manifest load + `Plan` chain-selection + artifact decode,
  reusing `Destination` and `Format`. Reusable by any consumer.
- Exported `ManifestObjectKey` / `ArtifactObjectKey` helpers (refactor of the
  writer's private key logic; no behaviour change to the writer).
- `SYNC_MODE_REPLAY_ARCHIVE` and the replication cold-replay path, with the hot
  journal pin and gapless position-based handoff.
- Fan-out target `archive` configuration (read-only).
- Reader unit tests (diff-only, snapshot-base, gap, unknown version, missing
  artifact) and an integration test for end-to-end cold replay + live handoff;
  docs (fan-out guide, gRPC API, configuration reference) and an operations
  runbook.

## Scope — out

- **The snapshotter binary stays write-only.** The reader is a library; the
  binary still does not serve queries (EDR-0001 unchanged). The only change to
  the write side is extracting shared key helpers.
- **No new `Format`, `Destination`, or `EventSink`.**
- **As-of / time-travel reads.** Point-in-time reconstruction is a natural next
  consumer of `Reader.Plan` but is its own EDR; this EDR only serves the
  resume-from-position case.
- **Compaction of the archive.** Retention stays manifest-driven in the
  snapshotter; the reader honours whatever the manifest lists.
- **Pushing cold artifacts into the hot journal.** Cold replay streams to the
  *client*; it never mutates the fan-out target's in-memory state or journal.

## Consequences

**Easier:**

- **Long-offline resume keeps history.** A client far behind the hot journal —
  an onboard node reconnecting after days offline, a regional replica, a service
  back from a long outage — resumes from cold storage with the changes it missed,
  instead of re-downloading the whole live table.
- **The archive earns its keep online.** The snapshotter's base+diff history,
  previously consumable only by bespoke offline jobs, becomes a first-class input
  to the live replication path.
- **Reusable reconstruction.** `Reader` is the tool EDR-0001 said consumers would
  need; time-travel/as-of reads and external loaders can build on the same code
  the fan-out uses.
- **No second archive.** We read the history we already write; operators run one
  archive, not two.

**Harder:**

- **Replication now reads the manifest contract.** A second component depends on
  the snapshotter's manifest/artifact format. The dependency is versioned and
  fail-safe (unknown version → fall back), but manifest changes now have two
  readers to consider.
- **A new operational coupling.** To benefit, an operator points the fan-out at
  the snapshotter's archive and keeps the snapshotter reasonably current; if it
  lags badly, cold replay silently declines to the live snapshot (observable via
  metrics, but a behaviour operators must understand).
- **Object-storage latency on the stale path.** Cold catch-up is slower than a
  hot delta. It runs off the live path and under the existing per-client
  backpressure, but a flood of simultaneous cold resumes (e.g. a fleet
  reconnecting at once) reads object storage hard; the runbook must cover it.

**New obligations:**

- **Fallback is load-bearing.** Every cold-path failure must fall back to a
  correct live full snapshot *before* any data is sent. This is the single most
  important invariant and gets explicit tests.
- **Manifest version is a read contract for replication too.** The reader pins
  `manifest_version`; a bump it does not understand means decline-and-fall-back,
  not best-effort parsing.
- **`Reader` ships with conformance tests** against the same `formattest` /
  `desttest` suites, so a new format or destination stays replayable, not just
  writable.

## References

- [EDR-0001 — Materialize a fan-out table to snapshot + diff artifacts](/edr/0001-snapshot-writer)
- [Fan-out guide](/guides/fan-out) — the protocol and the too-stale fallback this extends
- [gRPC API reference](/reference/grpc-api) — `Sync`, `SyncMode`
- `snapshotter/` — `Writer`, `Manifest`, `Artifact`, `Change`, `Format`, `Destination`
- `service/replication` — the `Sync` handler and source-position resume
- `target/fanout/journal.go` — the bounded hot journal and `ResumeSequenceForPosition`

## Changelog

- **2026-06-10**: Proposed.
