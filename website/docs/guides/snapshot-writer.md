---
sidebar_position: 13
title: Snapshot Writer
---

# Snapshot Writer (`laredo-snapshotter`)

`laredo-snapshotter` is a standalone process that subscribes to a
[fan-out](./fan-out.md) table and continuously writes it to durable storage as a
**base snapshot + a stream of diffs**, indexed by a **manifest**, so cold and
cross-account consumers can read the table from object storage on their own
schedule. For the design and the full picture, see
[Snapshot Writer — Architecture](../design/snapshot-writer.md).

## What it produces

For each table, under a key prefix on each destination:

```
config_document/
  manifest.json                 # the index: latest state + the artifact chain
  epoch=1/
    snapshot-0_19F000.jsonl     # base snapshot (full table at a WAL position)
    diff-0_19F000-0_1A2B3C.jsonl
    ...
  epoch=2/
    snapshot-0_2B0000.jsonl     # a re-base started a new epoch
    ...
```

A consumer reads `manifest.json`, loads the **newest snapshot**, and applies the
**diffs after it** to reconstruct the table as of `head_position`.

## Install

```bash
# from source
go build -o laredo-snapshotter ./cmd/laredo-snapshotter

# container image
docker pull ghcr.io/zourzouvillys/laredo-snapshotter:latest
```

## Configure

HOCON. A minimal local config (full example:
[`examples/snapshotter/`](https://github.com/zourzouvillys/laredo/tree/main/examples/snapshotter)):

```hocon
snapshotter {
  source { server = "localhost:4001", schema = public, table = test_users }
  diff     { interval = 5s }
  snapshot { min_interval = 30s, max_interval = 10m, max_churn_records = 1000 }
  destinations = [ { type = local, path = "./.laredo-archive" } ]
  formats { snapshot = [ jsonl ], diff = [ jsonl ] }
  http { port = 8080 }
}
```

Run it:

```bash
laredo-snapshotter --config snapshotter.conf
# or: LAREDO_SNAPSHOTTER_CONFIG=/etc/laredo/snapshotter.conf laredo-snapshotter
```

### Re-base thresholds

A diff is written every `diff.interval`; a fresh base snapshot is written instead
whenever a threshold fires (any one, subject to `min_interval`):

| Key | Meaning |
|---|---|
| `snapshot.min_interval` | Floor — never re-base more often than this |
| `snapshot.max_interval` | Ceiling — always re-base at least this often |
| `snapshot.max_diff_bytes` | Re-base when a serialized diff reaches this size |
| `snapshot.max_diff_fraction` | …or this fraction of the last snapshot's size |
| `snapshot.max_churn_records` | …or this many changed rows since the snapshot |
| `snapshot.max_churn_fraction` | …or this fraction of the dataset |

Omit a key to disable that trigger.

### Multiple tables

One process can materialize several tables — give a `tables` array, each entry a
full table block:

```hocon
snapshotter {
  http { port = 8080 }
  credentials { s3w { type = ambient } }
  tables = [
    { source { server = "laredo:4001", schema = public, table = users }
      destinations = [ { type = s3, bucket = b, prefix = "users/", credentials = s3w } ]
      formats { snapshot = [ jsonl, protobuf ], diff = [ protobuf ] } },
    { source { server = "laredo:4001", schema = public, table = orders }
      destinations = [ { type = local, path = "/var/lib/laredo/orders" } ] }
  ]
}
```

### Destinations, formats, events, credentials

- **Destinations** (`type = local | s3`) — an artifact is durable only once
  written to *all* destinations. S3 destinations name a credentials profile.
- **Formats** (`jsonl`, `protobuf`) — snapshots and diffs may differ, and you may
  emit several (each a separate object referenced from the manifest).
- **Events** (`sns`, `sqs`, `kinesis`) — advisory, at-least-once notifications
  published after the manifest head advances. Consumers must still poll the
  manifest as the source of truth.
- **Credentials** — named profiles referenced per AWS-backed component, so one
  process can use different roles for different actions:

```hocon
credentials {
  s3w { type = ambient }                                   # SDK default chain (env, IRSA, task role)
  pub { type = assume_role
        role_arn = "arn:aws:iam::222233334444:role/laredo-events-pub"
        external_id = "laredo" }
}
```

## Operate

`laredo-snapshotter` serves an HTTP API on `http.port`:

| Endpoint | Purpose |
|---|---|
| `GET /health/live` | Process is up |
| `GET /health/ready` | Every table has written its initial base snapshot |
| `GET /status` | Per-table position, epoch, buffer depth, churn, last snapshot |
| `POST /snapshot` | Force an immediate re-base on every table |
| `GET /metrics` | Prometheus: `snapshotter_epoch`, `snapshotter_buffer_depth`, `snapshotter_churn_records`, `snapshotter_snapshot_age_seconds` (per `table`), plus process/Go metrics |

On `SIGTERM`/`SIGINT` the writer flushes a final diff so no buffered changes are
lost. See the [runbook](../operations/snapshot-writer-runbook.md) for incident
procedures.
