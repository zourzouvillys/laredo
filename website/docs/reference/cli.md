---
sidebar_position: 2
title: CLI Reference
---

# CLI Reference

The `laredo` CLI tool speaks gRPC to a running `laredo-server` instance.

## Global flags

```
--address, -a    gRPC server address (default: localhost:4001, env: LAREDO_ADDRESS)
--tls            Enable TLS (default: false)
--cert           Path to TLS certificate
--timeout        Request timeout (default: 10s)
--output, -o     Output format: table, json, yaml (default: table)
```

## Commands

### `laredo status`

Show overall service status.

```bash
laredo status
laredo status --table public.config_document
```

### `laredo source [source_id]`

Show source details (slot info, lag, position).

```bash
laredo source pg_main
```

### `laredo pipelines`

List all pipelines with state, buffer depth, and error counts.

### `laredo tables`

List configured tables with their sources and targets.

### `laredo schema <schema.table>`

Show column definitions for a table.

### `laredo query`

Query in-memory targets.

```bash
# Lookup by primary index
laredo query public.config_document inst_abc settings/default

# Lookup by named index
laredo query public.config_document --index by_instance inst_abc

# Get by primary key
laredo query public.config_document --pk 42

# List all rows (paginated)
laredo query public.config_document --all --limit 10
```

### `laredo watch [schema.table]`

Stream status events in real time via the `WatchStatus` server-streaming RPC. Shows pipeline state changes, source connect/disconnect events, and row changes as they happen.

```bash
laredo watch                              # all events
laredo watch public.config_document       # filter by table
laredo watch --pipeline pg/public.users   # filter by pipeline ID
```

**Flags:**
| Flag | Description |
|---|---|
| `--table` | Filter by table (`schema.table`) |
| `--pipeline` | Filter by pipeline ID |

### `laredo reload`

Trigger a re-baseline.

```bash
laredo reload public.config_document
laredo reload --all
laredo reload --source pg_main --all
```

### `laredo pause` / `laredo resume`

Pause or resume sync.

```bash
laredo pause
laredo pause --source kinesis_events
laredo resume
```

### `laredo reset-source <source_id>`

Reset a source (drops and recreates slot/publication). Requires confirmation.

```bash
laredo reset-source pg_main
laredo reset-source pg_main --drop-publication
```

### `laredo snapshot`

Manage snapshots.

```bash
laredo snapshot create --meta config_version=2.1
laredo snapshot list
laredo snapshot inspect <id>
laredo snapshot restore <id>
laredo snapshot delete <id>
laredo snapshot prune --keep 3
```

### `laredo dead-letters`

Manage dead letter queue.

```bash
laredo dead-letters <pipeline_id>
laredo dead-letters replay <pipeline_id>
laredo dead-letters purge <pipeline_id>
```

### `laredo replay`

Replay a snapshot through a target for debugging.

```bash
laredo replay <snapshot_id> --pipeline <id> --speed full
```

### `laredo ready`

Check readiness. Exit code 0 if ready, 1 if not.

```bash
laredo ready
laredo ready --pipeline <id>
```

### `laredo fanout`

Inspect replication fan-out state.

```bash
laredo fanout status public.config_document
laredo fanout clients public.config_document
laredo fanout snapshots public.config_document
laredo fanout journal public.config_document --tail 5
```

### `laredo archive reconstruct`

Materialize a table's state as of a source position from the snapshotter's cold
archive. Reads object storage **directly** — no `laredo-server` connection — so
it works offline (forensics, an onboard node). See
[Snapshot Writer → Point-in-time reads](/guides/snapshot-writer#point-in-time-reads).

```bash
# Local archive
laredo archive reconstruct --store local --path /var/lib/laredo/archive/events \
  --key-prefix "public.events/" --format jsonl --key-fields id --at 0/1A2B3C0

# S3 archive (ambient AWS credentials)
laredo archive reconstruct --store s3 --bucket laredo-archive --prefix laredo/ \
  --region us-east-1 --key-prefix "public.events/" --at 0/1A2B3C0
```

| Flag | Description |
|---|---|
| `--at` | Source position (WAL LSN) to reconstruct as of (**required**) |
| `--store` | Archive store: `local` or `s3` (default: `local`) |
| `--path` | Local filesystem root (`store=local`) |
| `--bucket` / `--prefix` / `--region` | S3 destination (`store=s3`) |
| `--key-prefix` | Archive key prefix — **must match** the snapshotter's prefix for this table |
| `--format` | Artifact format: `jsonl` or `protobuf` (default: `jsonl`) |
| `--key-fields` | Comma-separated primary key columns (default: `id`) |

Prints `{position, row_count, rows}` as JSON, where `position` is the effective
artifact boundary at or before `--at`. Exits non-zero if the archive cannot
reach the requested position.
