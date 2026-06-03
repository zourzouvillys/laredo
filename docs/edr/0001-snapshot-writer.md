---
id: 1
title: "Materialize a fan-out table to durable snapshot + diff artifacts"
status: proposed
date: 2026-06-03
authors:
  - "Theo Zourzouvillys <theo@zrz.io>"
tags: [snapshot, diff, archive, s3, fan-out, aws]
supersedes: null
superseded_by: null
aliases: []
proposed_until: 2026-09-01
---

## TL;DR

We add a standalone binary, **`laredo-snapshotter`**, that connects to a laredo
fan-out service as a client, holds the table in memory, and continuously writes
the table's state to durable storage as a **base snapshot** plus a stream of
**diffs**. It writes an initial snapshot on first load, periodic diffs as changes
arrive, and a fresh snapshot whenever a configurable threshold is crossed (diff
size, churn, or age). Every artifact is recorded in a **manifest** so downstream
consumers can discover the latest state and reconstruct the table by reading the
newest snapshot and replaying the diffs after it.

Destinations (local FS, S3), artifact formats (JSONL, protobuf; parquet later),
and change-event sinks (SNS, SQS, Kinesis) are each **pluggable interfaces**.
AWS access uses `aws-sdk-go-v2` with ambient credentials and optional per-action
assume-role. The writer exposes an HTTP API to trigger a snapshot on demand and
to report status. It ships as a Docker image.

Reconstruction is the consumer's job and is out of scope for this binary — we
publish artifacts and a manifest; we do not serve queries.

## Context

laredo's fan-out target ([fan-out guide](../../website/docs/guides/fan-out.md))
already lets many clients hold a live in-memory replica of a PostgreSQL table
over gRPC. That is excellent for online consumers, but it does not produce a
**durable, offline-consumable** record of the table. Several needs are not met
today:

- **Cold consumers** (a Spark/Athena job, a data lake, a service in another
  region or account) want to read the table from object storage on their own
  schedule, without holding a long-lived gRPC stream.
- **Point-in-time history** — a sequence of snapshots and diffs is a cheap,
  append-only changelog that can be replayed or audited.
- **Cross-account / cross-region fan-out** — publishing to S3 + an event
  (SNS/SQS/Kinesis) is the lingua franca for decoupled, multi-account pipelines;
  a gRPC stream is not.

The forces that shape the design:

- The data already arrives change-by-change with a **stable source position**
  (the PostgreSQL WAL LSN, [EDR-0001 follow-on: cross-instance failover]). That
  position is the natural watermark to stamp on every artifact so consumers know
  exactly what they have.
- Writing a full snapshot on every change is wasteful; writing only diffs forever
  makes cold reads O(history). The classic answer is **base + incremental** with
  periodic re-basing — the open questions are *when* to re-base and *how* to let
  consumers discover the chain.
- Storage backends, file formats, and notification channels are deployment
  choices, not core logic. They must be swappable without touching the diff
  engine.
- AWS credential handling in multi-account setups is fiddly: the same process may
  need one role to write S3 and a different role to publish to a cross-account
  Kinesis stream. This must be expressible in configuration.

## Decision

We build `laredo-snapshotter`: a single-table[^multi] materializer that turns a
fan-out subscription into a base-plus-diff artifact stream with a manifest.

[^multi]: The core `Writer` is **per-table** — one subscription, one diff
    buffer, one threshold engine, one manifest — which keeps that logic simple.
    The binary, however, may host **several** per-table writers under a
    supervisor (each with its own destinations/formats/thresholds), so a single
    process can materialize multiple tables without the engine ever becoming
    multi-table. Sharing one subscription across tables is not planned.

### Pipeline

1. **Subscribe.** Use the existing `client/fanout` client to connect, load the
   initial snapshot, and stream changes. The client already tracks the stable
   source position per change; the writer reads it to watermark artifacts.
2. **Base snapshot on first ready.** When the client reports ready, serialize the
   full in-memory state to a **snapshot artifact** at the current source
   position, write it to every configured destination in every configured
   format, and record it in the manifest.
3. **Buffer changes.** Append every subsequent change (insert/update/delete) to
   an in-memory **diff buffer**, keyed by primary key so repeated edits to the
   same row within a window collapse to the net change.
4. **Flush on the diff interval.** Every `diff.interval`, if the buffer is
   non-empty, serialize it to a **diff artifact** spanning `(from_position,
   to_position]`, write it everywhere, record it in the manifest, and clear the
   buffer.
5. **Re-base on threshold.** Before flushing a diff, evaluate the **snapshot
   triggers**. If any fires, write a fresh base snapshot instead of a diff and
   reset the trigger counters. Triggers (any-of, all configurable):
   - **Diff size** — the serialized diff would exceed `snapshot.max_diff_bytes`,
     either absolute or as a fraction of the last snapshot's size
     (`snapshot.max_diff_fraction`).
   - **Churn** — cumulative changed-row count since the last snapshot exceeds
     `snapshot.max_churn_records`, or as a fraction of dataset size
     (`snapshot.max_churn_fraction`).
   - **Age** — time since the last snapshot exceeds `snapshot.max_interval`.
   - **Floor** — never snapshot more often than `snapshot.min_interval`
     (re-bases that would fire sooner are deferred; diffs continue).
6. **Manifest update.** After each artifact is durably written to *all*
   destinations, append it to the manifest and atomically publish the manifest's
   new head. The manifest is the single source of truth for "what is the latest
   state and how do I rebuild it."
7. **Notify.** After the manifest head advances, emit a **change event**
   (artifact kind, position range, URIs, sizes) to each configured event sink so
   pollers can be push-driven instead of polling.

### Pluggable seams

Each of these is a Go interface with small, named production implementations:

- **`Destination`** — where bytes land. `Put(ctx, key, reader)` plus manifest
  read/CAS-write. Implementations: **local filesystem**, **S3**. A writer may
  have *several* destinations; an artifact is durable only once written to all.
- **`Format`** — how an artifact is encoded. Separate format sets for snapshots
  and diffs (a snapshot and a diff need not share a format). Implementations:
  **JSONL**, **protobuf**. **Parquet** (snapshots) is a planned future
  implementation and the interface is shaped to accommodate it. A writer may
  emit *multiple* formats of the same artifact (e.g. JSONL for humans, protobuf
  for machines); each format is a separate object referenced from the manifest.
- **`EventSink`** — how consumers are told. `Publish(ctx, event)`.
  Implementations: **SNS**, **SQS**, **Kinesis**. Sinks are best-effort and
  off the durability path: a failed publish is logged and retried but never
  blocks or rolls back a written artifact (the manifest remains the source of
  truth; events are an optimization).

### Manifest

A small JSON document per table, stored at a well-known key on each destination
(e.g. `<prefix>/manifest.json`). It lists, newest-last, every live artifact:
its kind (`snapshot` | `diff`), source-position range, byte size, format → URI
map, and creation time, plus a monotonic `epoch` bumped on every snapshot so
consumers can detect a re-base. Manifest writes use **compare-and-swap** (S3
conditional `If-Match`/ETag; local atomic rename) so two writers cannot clobber
each other. A consumer rebuilds the table by taking the newest snapshot and
applying every diff whose range starts at or after it.

### AWS credentials

Credential resolution is per-**action group**, not global. The config defines
named credential profiles; each AWS-backed component (an S3 destination, a
Kinesis sink) names the profile it uses. A profile is one of: *ambient* (the
SDK default chain — env, web-identity, instance/task role), or *assume-role*
(an ARN plus optional external ID and session name, layered on top of an ambient
base). This lets one process write S3 under one role and publish to a
cross-account Kinesis stream under another, using `aws-sdk-go-v2`'s
`stscreds.AssumeRoleProvider`.

### Configuration

HOCON, loaded with the existing `config` package conventions (file + conf.d +
env + `--set`). The diff/snapshot policy is fully data-driven:
`diff.interval`, `snapshot.{min,max}_interval`, `snapshot.max_diff_bytes`,
`snapshot.max_diff_fraction`, `snapshot.max_churn_records`,
`snapshot.max_churn_fraction`, plus the destination/format/sink/credential
blocks. Example files ship under `examples/snapshotter/`.

### Operational surface

An HTTP server exposes: `GET /health/live`, `GET /health/ready`,
`GET /metrics` (Prometheus, via the existing observer pattern), `GET /status`
(current position, last snapshot/diff, buffer depth, manifest head), and
`POST /snapshot` (force a base snapshot now). Signals and graceful shutdown
mirror `laredo-server`; on shutdown the writer flushes a final diff.

### Scope — in

- One binary materializing one table to base + diff artifacts with a manifest.
- `Destination` (local, S3), `Format` (JSONL, protobuf), `EventSink` (SNS, SQS,
  Kinesis) interfaces + named implementations.
- Configurable snapshot/diff thresholds; on-demand snapshot API.
- Per-action AWS credential profiles (ambient + assume-role).
- Docker image, example configs, metrics, health, docs, runbook.

### Scope — out

- **Reconstruction / query serving.** Consumers rebuild from artifacts; this
  binary never reads its own output back to answer queries.
- **Multi-table processes.** One table per process for now.
- **Parquet.** Interface accommodates it; implementation is later.
- **Compaction of old diffs** beyond manifest-driven retention (prune artifacts
  older than the newest snapshot they precede). True log compaction is future.
- **Exactly-once event delivery.** Events are at-least-once and advisory.

## Consequences

**Easier:**

- **Cold and cross-account consumption.** Any system that can read object storage
  and parse JSONL/protobuf can consume the table on its own schedule, with a
  manifest telling it exactly what to read and the source position it represents.
- **Cheap history & audit.** The diff stream is an append-only changelog with
  WAL-position watermarks; replay and point-in-time inspection fall out for free.
- **Backend independence.** Storage, format, and notification are swapped in
  config; the diff engine never changes. Adding parquet or EventBridge later is
  a new implementation of an existing interface.
- **Reuse.** We build on `client/fanout` (already does snapshot+resume+failover)
  and the AWS SDK patterns already in `source/kinesis` and `snapshot/s3`.

**Harder:**

- **Another moving part to operate.** A new long-lived process per table, with
  its own credentials, memory footprint (it holds the full table), and failure
  modes. The runbook must cover stuck manifests, destination outages, and
  credential expiry.
- **Threshold tuning.** The base-vs-diff trade-off is workload-specific; bad
  thresholds mean either huge cold-read chains or wasteful snapshotting.
  Defaults plus metrics (`diff_bytes`, `churn_records`, `snapshot_age`) make this
  observable, but it is a knob operators must own.
- **Multi-destination atomicity.** "Durable only when written to all
  destinations" means a partial write must be retried or rolled forward; we
  accept brief windows where one destination is ahead, reconciled by the manifest
  CAS being the commit point.

**New obligations:**

- **Manifest is load-bearing.** Its format is a compatibility contract with every
  consumer. Changes are versioned (`manifest_version`) and additive; breaking
  changes require a new EDR.
- **Credentials are least-privilege per action.** Each profile gets only the
  permissions its action group needs (S3 write to one prefix; Kinesis put to one
  stream). Documented in the deployment guide.
- **Events are advisory, never authoritative.** Consumers must tolerate missing or
  duplicated events and fall back to polling the manifest. We document this
  explicitly so no one builds exactly-once assumptions on them.
- **Every new `Format`/`Destination`/`EventSink` ships with a conformance test**
  against a shared interface test suite, so implementations stay interchangeable.

## References

- [Fan-out guide](../../website/docs/guides/fan-out.md) — the client this builds on
- [Snapshot Writer architecture](../../website/docs/design/snapshot-writer.md) — diagrams & how it works
- `client/fanout` — the subscription client (snapshot + resume + failover)
- `snapshot/`, `snapshot/jsonl`, `snapshot/s3` — existing serialization/store patterns to model on
- `source/kinesis` — existing `aws-sdk-go-v2` client + assume-role patterns

## Changelog

- **2026-06-03**: Proposed.
