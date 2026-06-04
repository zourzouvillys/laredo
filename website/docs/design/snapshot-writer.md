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

<svg class="diagram" viewBox="0 0 720 400" role="img" aria-label="PostgreSQL feeds laredo-server; over gRPC it serves online consumers and laredo-snapshotter; the snapshotter writes snapshots, diffs and a manifest to destinations and emits events; cold consumers poll the manifest.">
  <defs><marker id="sw-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 L10 5 L0 10 z" fill="var(--fg-muted)"/></marker></defs>
  <!-- postgres -->
  <rect x="276" y="8" width="168" height="36" rx="9" fill="var(--bg-subtle)" stroke="var(--allow)" stroke-width="1.5"/>
  <text x="360" y="31" text-anchor="middle" fill="var(--allow)" font-size="11.5" font-weight="700">PostgreSQL · logical replication</text>
  <line x1="360" y1="44" x2="360" y2="62" stroke="var(--fg-muted)" stroke-width="1.6" marker-end="url(#sw-arrow)"/>
  <!-- laredo-server -->
  <rect x="276" y="64" width="168" height="42" rx="9" fill="var(--bg-subtle)" stroke="var(--accent)" stroke-width="2"/>
  <text x="360" y="83" text-anchor="middle" fill="var(--accent)" font-size="12" font-weight="700">laredo-server</text>
  <text x="360" y="98" text-anchor="middle" fill="var(--fg-muted)" font-size="10">fan-out target</text>
  <text x="360" y="124" text-anchor="middle" fill="var(--fg-faint)" font-size="9.5">gRPC Sync — snapshot + live changes</text>
  <!-- gRPC consumers row -->
  <g stroke="var(--fg-muted)" stroke-width="1.5" fill="none" marker-end="url(#sw-arrow)">
    <path d="M320 106 C 240 130, 150 130, 130 150"/>
    <path d="M360 106 L 360 150"/>
    <path d="M400 106 C 480 130, 580 130, 600 150"/>
  </g>
  <rect x="56" y="152" width="148" height="40" rx="9" fill="var(--bg-muted)" stroke="var(--border)"/>
  <text x="130" y="170" text-anchor="middle" fill="var(--fg-secondary)" font-size="11" font-weight="700">online consumer</text>
  <text x="130" y="184" text-anchor="middle" fill="var(--fg-muted)" font-size="9.5">client/fanout</text>
  <rect x="286" y="152" width="148" height="40" rx="9" fill="var(--bg-muted)" stroke="var(--border)"/>
  <text x="360" y="170" text-anchor="middle" fill="var(--fg-secondary)" font-size="11" font-weight="700">online consumer</text>
  <text x="360" y="184" text-anchor="middle" fill="var(--fg-muted)" font-size="9.5">client/fanout</text>
  <rect x="516" y="148" width="168" height="58" rx="10" fill="var(--sensitive-soft)" stroke="var(--sensitive)" stroke-width="1.8"/>
  <text x="600" y="167" text-anchor="middle" fill="var(--sensitive)" font-size="11.5" font-weight="700">laredo-snapshotter</text>
  <text x="600" y="182" text-anchor="middle" fill="var(--fg-secondary)" font-size="9.5">in-mem replica · diff buffer</text>
  <text x="600" y="195" text-anchor="middle" fill="var(--fg-secondary)" font-size="9.5">threshold engine</text>
  <!-- snapshotter → outputs -->
  <text x="600" y="224" text-anchor="middle" fill="var(--fg-faint)" font-size="9">writes artifacts + manifest · emits events</text>
  <g stroke="var(--fg-muted)" stroke-width="1.5" fill="none" marker-end="url(#sw-arrow)">
    <path d="M560 206 C 360 240, 200 250, 150 268"/>
    <path d="M600 206 L 380 268"/>
    <path d="M620 206 C 660 240, 640 250, 600 268"/>
  </g>
  <rect x="56" y="270" width="190" height="44" rx="9" fill="var(--bg-subtle)" stroke="var(--border)"/>
  <text x="151" y="289" text-anchor="middle" fill="var(--fg)" font-size="11" font-weight="700">Destinations</text>
  <text x="151" y="303" text-anchor="middle" fill="var(--fg-muted)" font-size="9.5">local FS · S3</text>
  <rect x="288" y="270" width="170" height="44" rx="9" fill="var(--bg-subtle)" stroke="var(--accent)" stroke-width="1.5"/>
  <text x="373" y="289" text-anchor="middle" fill="var(--accent)" font-size="11" font-weight="700">Manifest</text>
  <text x="373" y="303" text-anchor="middle" fill="var(--fg-muted)" font-size="9.5">latest head · the chain</text>
  <rect x="500" y="270" width="184" height="44" rx="9" fill="var(--bg-subtle)" stroke="var(--border)"/>
  <text x="592" y="289" text-anchor="middle" fill="var(--fg)" font-size="11" font-weight="700">Event sinks</text>
  <text x="592" y="303" text-anchor="middle" fill="var(--fg-muted)" font-size="9.5">SNS · SQS · Kinesis</text>
  <!-- cold consumers caption -->
  <text x="360" y="350" text-anchor="middle" fill="var(--fg-secondary)" font-size="11">cold consumers (Athena, Spark, another account/region)</text>
  <text x="360" y="368" text-anchor="middle" fill="var(--fg-muted)" font-size="10.5">poll the manifest → read newest snapshot + following diffs, or react to events</text>
</svg>

The writer is just another `client/fanout` consumer — it benefits from the same
snapshot bootstrap, LSN-based resume, and cross-instance failover as any online
client. The difference is what it *does* with the stream: it persists it.

## The base + diff model

A consumer should never have to replay all history. The writer keeps cold reads
cheap by periodically **re-basing**: writing a fresh full snapshot and starting a
new diff chain after it.

<iframe src="/laredo/viz/snapshot-writer.html?embed=1" title="Base + diff timeline with threshold-driven re-base" loading="lazy" class="embed"></iframe>

A base snapshot opens an **epoch**; periodic **diffs** follow it, each covering a
half-open source-position range `(from, to]`. When a threshold fires, the writer
re-bases — a fresh snapshot starts a new epoch. To rebuild the table at the
latest position a consumer reads the **newest snapshot** and applies every diff
after it; everything in an older epoch can be pruned once the next snapshot is
durable.

Each artifact covers a half-open source-position range `(from, to]`. Snapshots
are stamped with the position at which the full state was captured. Diffs are
contiguous: the next diff's `from` equals the previous artifact's `to`, so a
consumer can verify it has an unbroken chain.

## Internal data flow

<svg class="diagram" viewBox="0 0 720 470" role="img" aria-label="Data flow: the client store yields a base snapshot on ready and buffers changes; the threshold engine writes a diff or re-bases; artifacts are encoded and written to all destinations; the manifest compare-and-swap is the commit point; then an event is emitted." style="max-width:680px">
  <defs><marker id="df-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 L10 5 L0 10 z" fill="var(--fg-muted)"/></marker></defs>
  <style>.df text{font-size:11px} .df .t{font-weight:700;font-size:11.5px} .df .e{stroke:var(--fg-muted);stroke-width:1.5;fill:none;marker-end:url(#df-arrow)} .df .el{font-size:9.5px;fill:var(--fg-muted)}</style>
  <g class="df">
    <!-- client/fanout -->
    <rect x="30" y="24" width="150" height="52" rx="9" fill="var(--bg-muted)" stroke="var(--border)"/>
    <text x="105" y="46" text-anchor="middle" class="t" fill="var(--fg-secondary)">client/fanout</text>
    <text x="105" y="62" text-anchor="middle" fill="var(--fg-muted)">in-mem store · Listen()</text>
    <!-- base snapshot -->
    <rect x="470" y="24" width="220" height="44" rx="9" fill="var(--sensitive-soft)" stroke="var(--sensitive)" stroke-width="1.5"/>
    <text x="580" y="51" text-anchor="middle" class="t" fill="var(--sensitive)">base snapshot (full state)</text>
    <path class="e" d="M180 44 L 466 44"/><text x="320" y="36" text-anchor="middle" class="el">OnReady</text>
    <!-- diff buffer -->
    <rect x="30" y="120" width="150" height="56" rx="9" fill="var(--queue-soft)" stroke="var(--queue)" stroke-width="1.5"/>
    <text x="105" y="142" text-anchor="middle" class="t" fill="var(--queue)">diff buffer</text>
    <text x="105" y="158" text-anchor="middle" fill="var(--fg-muted)">keyed by PK,</text>
    <text x="105" y="170" text-anchor="middle" fill="var(--fg-muted)">net change</text>
    <path class="e" d="M105 76 L 105 116"/><text x="113" y="100" class="el">change(old,new) + position</text>
    <!-- threshold engine -->
    <rect x="270" y="118" width="150" height="60" rx="9" fill="var(--bg-subtle)" stroke="var(--accent)" stroke-width="1.8"/>
    <text x="345" y="142" text-anchor="middle" class="t" fill="var(--accent)">threshold engine</text>
    <text x="345" y="160" text-anchor="middle" fill="var(--fg-muted)">size / churn / age?</text>
    <path class="e" d="M180 148 L 266 148"/><text x="223" y="140" text-anchor="middle" class="el">diff.interval</text>
    <!-- re-base -->
    <rect x="500" y="126" width="160" height="44" rx="9" fill="var(--sensitive-soft)" stroke="var(--sensitive)" stroke-width="1.5"/>
    <text x="580" y="153" text-anchor="middle" class="t" fill="var(--sensitive)">re-base: snapshot</text>
    <path class="e" d="M420 148 L 496 148"/><text x="458" y="140" text-anchor="middle" class="el">yes</text>
    <!-- diff artifact -->
    <rect x="270" y="230" width="150" height="44" rx="9" fill="var(--main-soft)" stroke="var(--main)" stroke-width="1.5"/>
    <text x="345" y="257" text-anchor="middle" class="t" fill="var(--main)">diff artifact</text>
    <path class="e" d="M345 178 L 345 226"/><text x="353" y="206" class="el">no</text>
    <!-- encode/write -->
    <rect x="150" y="312" width="420" height="42" rx="9" fill="var(--bg-subtle)" stroke="var(--border)" stroke-width="1.5"/>
    <text x="360" y="338" text-anchor="middle" class="t" fill="var(--fg)">encode (Format × N) → write (Dest × N)</text>
    <g class="e"><path d="M580 68 C 580 200, 470 280, 420 312"/><path d="M580 170 C 560 240, 470 290, 430 312"/><path d="M345 274 L 345 308"/></g>
    <!-- manifest CAS -->
    <rect x="246" y="384" width="228" height="42" rx="9" fill="var(--accent-light)" stroke="var(--accent)" stroke-width="2"/>
    <text x="360" y="410" text-anchor="middle" class="t" fill="var(--accent)">manifest CAS append</text>
    <path class="e" d="M360 354 L 360 380"/><text x="368" y="372" class="el">all destinations durable</text>
    <text x="486" y="409" fill="var(--accent)" font-size="10" font-weight="700">← commit point</text>
    <!-- emit -->
    <rect x="276" y="436" width="168" height="30" rx="8" fill="var(--bg-muted)" stroke="var(--border)"/>
    <text x="360" y="455" text-anchor="middle" fill="var(--fg-secondary)" font-size="11">emit event (sinks)</text>
    <path class="e" d="M360 426 L 360 432"/>
  </g>
</svg>

The **manifest CAS append** is the commit point. An artifact's bytes may exist on
storage before the manifest references it (a crash there just leaves an orphan to
be garbage-collected); it is only *live* once the manifest points at it. Events
fire after the commit and are advisory.

## Snapshot-vs-diff decision

Before every scheduled flush, the threshold engine decides whether to write a
diff or re-base. The triggers are independent and any one fires a snapshot,
subject to a minimum-interval floor:

<svg class="diagram" viewBox="0 0 720 360" role="img" aria-label="On each flush tick: if within the min-interval floor, write a diff; otherwise if any size, churn, or age trigger fired, write a snapshot (re-base), else write a diff." style="max-width:660px">
  <defs><marker id="dc-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 L10 5 L0 10 z" fill="var(--fg-muted)"/></marker></defs>
  <style>.dc text{font-size:11px} .dc .e{stroke:var(--fg-muted);stroke-width:1.5;fill:none;marker-end:url(#dc-arrow)} .dc .el{font-size:10px;fill:var(--fg-muted);font-weight:700}</style>
  <g class="dc">
    <!-- flush tick -->
    <rect x="285" y="14" width="150" height="32" rx="16" fill="var(--bg-subtle)" stroke="var(--accent)" stroke-width="1.6"/>
    <text x="360" y="34" text-anchor="middle" fill="var(--accent)" font-weight="700">flush tick</text>
    <!-- decision 1: min_interval -->
    <path class="e" d="M360 46 L 360 70"/>
    <rect x="232" y="72" width="256" height="40" rx="9" fill="var(--bg-subtle)" stroke="var(--border)" stroke-width="1.5"/>
    <text x="360" y="97" text-anchor="middle" fill="var(--fg)">since snapshot &lt; min_interval?</text>
    <!-- yes → diff (right) -->
    <path class="e" d="M488 92 L 560 92"/><text x="524" y="84" text-anchor="middle" class="el">yes</text>
    <rect x="560" y="74" width="140" height="36" rx="9" fill="var(--main-soft)" stroke="var(--main)" stroke-width="1.5"/>
    <text x="630" y="97" text-anchor="middle" fill="var(--main)" font-weight="700">write DIFF</text>
    <!-- no: triggers panel -->
    <path class="e" d="M360 112 L 360 134"/><text x="372" y="128" class="el">no</text>
    <rect x="170" y="136" width="380" height="118" rx="10" fill="var(--bg-muted)" stroke="var(--border)"/>
    <text x="188" y="156" fill="var(--fg)" font-weight="700">any of:</text>
    <text x="200" y="176" fill="var(--fg-secondary)">• serialized diff size &gt; max_diff_bytes</text>
    <text x="200" y="194" fill="var(--fg-secondary)">• diff size &gt; max_diff_fraction × snapshot_size</text>
    <text x="200" y="212" fill="var(--fg-secondary)">• churn records &gt; max_churn_records</text>
    <text x="200" y="230" fill="var(--fg-secondary)">• churn / dataset &gt; max_churn_fraction</text>
    <text x="200" y="248" fill="var(--fg-secondary)">• age since snapshot &gt; max_interval</text>
    <!-- branch -->
    <path class="e" d="M300 254 C 260 280, 200 286, 175 300"/><text x="232" y="280" text-anchor="middle" class="el">yes</text>
    <path class="e" d="M430 254 C 500 280, 560 286, 590 300"/><text x="520" y="280" text-anchor="middle" class="el">no</text>
    <rect x="30" y="302" width="190" height="48" rx="9" fill="var(--sensitive-soft)" stroke="var(--sensitive)" stroke-width="1.8"/>
    <text x="125" y="322" text-anchor="middle" fill="var(--sensitive)" font-weight="700">write SNAPSHOT</text>
    <text x="125" y="338" text-anchor="middle" fill="var(--fg-muted)" font-size="9.5">reset counters · bump epoch</text>
    <rect x="520" y="306" width="140" height="36" rx="9" fill="var(--main-soft)" stroke="var(--main)" stroke-width="1.5"/>
    <text x="590" y="329" text-anchor="middle" fill="var(--main)" font-weight="700">write DIFF</text>
  </g>
</svg>

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
