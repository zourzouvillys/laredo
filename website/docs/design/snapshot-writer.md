---
sidebar_position: 1
title: Snapshot Writer — Architecture
---

# Snapshot Writer — Architecture & Design

> **Status:** proposed — see [EDR-0001](https://github.com/zourzouvillys/laredo/blob/main/docs/edr/0001-snapshot-writer.md).
> This page explains *how it works*; the EDR records *what we decided and why*.

The **Snapshot Writer** (`laredo-snapshotter`) is a standalone process that turns
a live fan-out subscription into a durable, offline-consumable archive of a
table: a **base snapshot** plus a stream of **diffs**, indexed by a **manifest**,
written to pluggable storage in pluggable formats, with optional change events.

It does **not** serve queries. It publishes artifacts; consumers read them.

## Where it fits

```
                          PostgreSQL (logical replication)
                                      │
                          ┌───────────▼───────────┐
                          │     laredo-server      │
                          │  (fan-out target)      │
                          └───────────┬───────────┘
                            gRPC Sync │ (snapshot + live changes)
              ┌───────────────────────┼───────────────────────┐
              ▼                       ▼                         ▼
       online consumer         online consumer         ┌───────────────────┐
      (client/fanout)         (client/fanout)           │ laredo-snapshotter │
                                                        │  in-mem replica    │
                                                        │  diff buffer       │
                                                        │  threshold engine  │
                                                        └─────────┬─────────┘
                                       write snapshots/diffs/manifest │ emit events
                          ┌─────────────────────────────┬────────────┴────────┐
                          ▼                              ▼                      ▼
                 ┌─────────────────┐          ┌──────────────────┐    ┌────────────────┐
                 │  Destinations    │          │   Manifest        │    │  Event sinks    │
                 │  local FS │ S3   │          │  (latest head)    │    │ SNS│SQS│Kinesis │
                 └────────┬────────┘          └────────┬─────────┘    └───────┬────────┘
                          ▼                            ▼                       ▼
                 cold consumers (Athena, Spark, another account/region) poll manifest,
                 read newest snapshot + following diffs, or react to events.
```

The writer is just another `client/fanout` consumer — it benefits from the same
snapshot bootstrap, LSN-based resume, and cross-instance failover as any online
client. The difference is what it *does* with the stream: it persists it.

## The base + diff model

A consumer should never have to replay all history. The writer keeps cold reads
cheap by periodically **re-basing**: writing a fresh full snapshot and starting a
new diff chain after it.

```
 source position (WAL LSN) ───────────────────────────────────────────────▶

 epoch 1                              epoch 2
 ┌─────────┐  ┌────┐ ┌────┐ ┌────┐    ┌─────────┐  ┌────┐ ┌────┐ ┌────┐ ...
 │SNAPSHOT │  │diff│ │diff│ │diff│    │SNAPSHOT │  │diff│ │diff│ │diff│
 │ @100    │  │101-│ │121-│ │141-│    │ @173    │  │174-│ │190-│ │205-│
 │ (full)  │  │120 │ │140 │ │172 │    │ (full)  │  │189 │ │204 │ │ .. │
 └─────────┘  └────┘ └────┘ └────┘    └─────────┘  └────┘ └────┘ └────┘
      ▲                        ▲           ▲
      │                        │           └── a threshold fired → re-base
      │                        └── periodic diff flush (diff.interval)
      └── initial base snapshot on first ready

 To rebuild the table at the latest position, a consumer reads the NEWEST
 snapshot (epoch 2 @173) and applies every diff after it (174-189, 190-204, …).
 Everything in epoch 1 can be pruned once epoch 2's snapshot is durable.
```

Each artifact covers a half-open source-position range `(from, to]`. Snapshots
are stamped with the position at which the full state was captured. Diffs are
contiguous: the next diff's `from` equals the previous artifact's `to`, so a
consumer can verify it has an unbroken chain.

## Internal data flow

```
        client/fanout
        ┌──────────────┐   OnReady          ┌───────────────────────────┐
        │ in-mem store │ ─────────────────▶ │  base snapshot (full state)│
        │  + Listen()  │                    └─────────────┬─────────────┘
        └──────┬───────┘                                  │
   change(old,new) + source position                     │
               │                                          │
               ▼                                          │
        ┌──────────────┐  every diff.interval             │
        │  diff buffer  │ ───────────┐                    │
        │ (keyed by PK, │            ▼                     ▼
        │  net change)  │     ┌─────────────┐      ┌──────────────┐
        └──────┬───────┘     │  threshold   │ yes  │  re-base:    │
               │             │  engine:     │─────▶│  snapshot    │
               │             │  size/churn/ │      └──────┬───────┘
               │             │  age?        │  no         │
               │             └──────┬──────┘             │
               │                    ▼                     │
               │             ┌─────────────┐              │
               └────────────▶│ diff artifact│             │
                             └──────┬──────┘              │
                                    ▼                     ▼
                        ┌────────────────────────────────────────┐
                        │  encode (Format × N) → write (Dest × N)  │
                        └───────────────────┬────────────────────┘
                                            ▼   all destinations durable
                                  ┌───────────────────┐
                                  │ manifest CAS append│  ← commit point
                                  └─────────┬─────────┘
                                            ▼
                                  ┌───────────────────┐
                                  │ emit event (sinks) │  ← best-effort
                                  └───────────────────┘
```

The **manifest CAS append** is the commit point. An artifact's bytes may exist on
storage before the manifest references it (a crash there just leaves an orphan to
be garbage-collected); it is only *live* once the manifest points at it. Events
fire after the commit and are advisory.

## Snapshot-vs-diff decision

Before every scheduled flush, the threshold engine decides whether to write a
diff or re-base. The triggers are independent and any one fires a snapshot,
subject to a minimum-interval floor:

```
                        ┌─────────────── flush tick ───────────────┐
                        ▼                                           │
            since last snapshot < min_interval? ──yes──▶ write DIFF │
                        │ no                                        │
                        ▼                                           │
      ANY of:                                                       │
        • serialized diff size  > max_diff_bytes                    │
        • serialized diff size  > max_diff_fraction × snapshot_size │
        • churn records         > max_churn_records                 │
        • churn / dataset_size  > max_churn_fraction                │
        • age since snapshot    > max_interval                      │
                        │                                           │
              ┌── yes ──┴── no ──┐                                  │
              ▼                  ▼                                  │
         write SNAPSHOT      write DIFF ───────────────────────────┘
         (reset counters,
          bump epoch)
```

`min_interval` prevents snapshot storms on bursty tables; `max_interval`
guarantees a re-base eventually happens even on a quiet table so cold-read chains
stay bounded. All values are configurable; sensible defaults ship in the example
config.

## Manifest

One JSON document per table, at a well-known key on each destination
(`<prefix>/manifest.json`). It is the contract between the writer and every
consumer.

```json
{
  "manifest_version": 1,
  "table": "public.config_document",
  "epoch": 2,
  "updated_at": "2026-06-03T21:40:11Z",
  "head_position": "0/1A2B3C",
  "artifacts": [
    {
      "kind": "snapshot",
      "epoch": 2,
      "from_position": null,
      "to_position": "0/19F000",
      "created_at": "2026-06-03T21:30:00Z",
      "row_count": 48211,
      "formats": {
        "jsonl":    { "uri": "s3://bucket/cfg/epoch=2/snapshot-0_19F000.jsonl", "size_bytes": 7340032 },
        "protobuf": { "uri": "s3://bucket/cfg/epoch=2/snapshot-0_19F000.pb",    "size_bytes": 4194304 }
      }
    },
    {
      "kind": "diff",
      "epoch": 2,
      "from_position": "0/19F000",
      "to_position": "0/1A2B3C",
      "created_at": "2026-06-03T21:40:00Z",
      "change_count": 312,
      "formats": {
        "jsonl":    { "uri": "s3://bucket/cfg/epoch=2/diff-0_19F000-0_1A2B3C.jsonl", "size_bytes": 81920 },
        "protobuf": { "uri": "s3://bucket/cfg/epoch=2/diff-0_19F000-0_1A2B3C.pb",    "size_bytes": 49152 }
      }
    }
  ]
}
```

**Concurrency:** manifest writes are compare-and-swap — S3 conditional writes
(`If-Match` on the ETag) and atomic `rename(2)` on local FS — so a misconfigured
second writer cannot silently corrupt the chain; the loser retries against the
new head or aborts.

### How a consumer rebuilds the table

```
1. GET manifest.json
2. snapshot ← the artifact with kind=snapshot and the highest epoch
3. load snapshot (pick any format you can read) → in-memory map keyed by PK
4. for each diff where diff.epoch == snapshot.epoch, in position order:
        apply inserts/updates/deletes by PK
5. the result is the table as of manifest.head_position
   (poll the manifest, or subscribe to events, to stay current)
```

Because diffs are keyed by primary key and contiguous, replay is deterministic
and idempotent — re-applying a diff a consumer already has is a no-op.

## Pluggable seams

Three interfaces keep storage, encoding, and notification independent of the diff
engine. Each implementation ships with a shared conformance test so they stay
interchangeable.

```go
// Destination is where artifact bytes land, and where the manifest lives.
type Destination interface {
    Put(ctx context.Context, key string, body io.Reader) (uri string, size int64, err error)
    // ReadManifest returns the current manifest and an opaque revision token.
    ReadManifest(ctx context.Context, table string) (data []byte, rev string, err error)
    // WriteManifest writes iff rev matches the current revision (compare-and-swap).
    WriteManifest(ctx context.Context, table string, data []byte, rev string) (newRev string, err error)
}

// Format encodes one artifact. Snapshots and diffs may use different formats.
type Format interface {
    FormatID() string                                  // "jsonl" | "protobuf" | …
    Extension() string                                 // ".jsonl" | ".pb"
    WriteSnapshot(w io.Writer, rows iter.Seq[laredo.Row]) error
    WriteDiff(w io.Writer, changes iter.Seq[Change]) error
}

// EventSink notifies consumers that the manifest head advanced. Best-effort.
type EventSink interface {
    Publish(ctx context.Context, ev ArtifactEvent) error
}
```

Implementations shipping in v1:

| Seam | Implementations | Future |
|---|---|---|
| `Destination` | local filesystem, S3 | GCS, Azure Blob |
| `Format` (snapshot) | JSONL, protobuf | **parquet** |
| `Format` (diff) | JSONL, protobuf | — |
| `EventSink` | SNS, SQS, Kinesis | EventBridge |

A writer may attach **several** destinations (an artifact is durable only once
written to all) and **several** formats (each is a separate object referenced
from the manifest), so you can, e.g., publish protobuf for services and JSONL for
ad-hoc inspection from the same run.

## AWS credentials — per action

Credential resolution is per **action group**, not global. Named profiles are
defined once and referenced by each AWS-backed component, so one process can
write S3 under one role and publish to a cross-account Kinesis stream under
another.

```
credentials {
  s3_writer   { type = ambient }                       # SDK default chain (env, IRSA, task role)
  kinesis_pub { type = assume_role
                role_arn = "arn:aws:iam::222233334444:role/laredo-events-pub"
                external_id = "laredo"
                session_name = "laredo-snapshotter" }   # layered on the ambient base
}
```

Built on `aws-sdk-go-v2` (`config.LoadDefaultConfig` for ambient,
`stscreds.AssumeRoleProvider` for assume-role) — the same library already used by
`source/kinesis` and `snapshot/s3`.

## Change events

After the manifest head advances, an `ArtifactEvent` is published to each sink:

```json
{
  "table": "public.config_document",
  "kind": "snapshot",
  "epoch": 2,
  "from_position": null,
  "to_position": "0/19F000",
  "head_position": "0/19F000",
  "manifest_uri": "s3://bucket/cfg/manifest.json",
  "formats": { "jsonl": "s3://…/snapshot-….jsonl", "protobuf": "s3://…/snapshot-….pb" },
  "created_at": "2026-06-03T21:30:00Z"
}
```

Events are **at-least-once and advisory**: a consumer must tolerate gaps and
duplicates and fall back to polling the manifest. They exist to make pollers
push-driven, not to be an authoritative log.

## Configuration

See [the example configs](https://github.com/zourzouvillys/laredo/tree/main/examples/snapshotter)
for complete, commented files. The shape:

```hocon
snapshotter {
  source   { server = "laredo-server:4001", schema = public, table = config_document, client_id = snapshotter-1 }

  diff      { interval = 30s }                # flush buffered changes this often

  snapshot {
    min_interval        = 5m                  # never re-base more often than this
    max_interval        = 6h                  # always re-base at least this often
    max_diff_bytes      = 33554432            # 32 MiB serialized diff → re-base
    max_diff_fraction   = 0.25                # …or 25% of the last snapshot's size
    max_churn_records   = 100000              # …or this many changed rows
    max_churn_fraction  = 0.5                 # …or 50% of the dataset
  }

  destinations = [
    { type = s3,    bucket = my-bucket, prefix = "cfg/", credentials = s3_writer },
    { type = local, path = "/var/lib/laredo/archive" }
  ]

  formats {
    snapshot = [ jsonl, protobuf ]            # emit both
    diff     = [ protobuf ]
  }

  events = [
    { type = sns,     topic_arn  = "arn:aws:sns:us-east-1:111:laredo-cfg",    credentials = kinesis_pub },
    { type = kinesis, stream     = "laredo-cfg",                              credentials = kinesis_pub }
  ]

  credentials { /* named profiles, see AWS section */ }

  http { port = 8080 }                        # health, metrics, /status, POST /snapshot
}
```

Every threshold is independently tunable; omit a trigger to disable it.

## Operations

- **Metrics** (Prometheus, `/metrics`): `snapshotter_artifacts_written_total{kind}`,
  `snapshotter_diff_bytes`, `snapshotter_churn_records`,
  `snapshotter_snapshot_age_seconds`, `snapshotter_buffer_depth`,
  `snapshotter_manifest_head_position`, `snapshotter_destination_errors_total{dest}`,
  `snapshotter_event_errors_total{sink}`.
- **Health:** `/health/ready` is true once the initial base snapshot is durable.
- **Status:** `GET /status` returns current position, last snapshot/diff, buffer
  depth, epoch, and manifest head.
- **On-demand snapshot:** `POST /snapshot` forces a re-base now (e.g. before a
  schema migration or a consumer cutover).
- **Shutdown:** flushes a final diff so no buffered changes are lost.

A full operational runbook (stuck manifest CAS, destination outage, credential
expiry, threshold tuning) ships under
[Operations → Snapshot Writer runbook](../operations/snapshot-writer-runbook.md)
when the feature lands.

## Status & roadmap

Implementation is phased:

1. ✅ Interfaces + JSONL format + local destination + threshold engine + manifest (no AWS).
2. ✅ S3 destination + protobuf format + per-action credentials.
3. ✅ Event sinks (SNS, SQS, Kinesis).
4. ✅ `cmd/laredo-snapshotter` + HTTP API + Docker image + example configs.
5. ⏳ Parquet snapshot format (future).

See the [Snapshot Writer guide](../guides/snapshot-writer.md) to configure and run it.
```
