---
sidebar_position: 12
title: Archive Source
---

# Archive Source

The **archive source** (`source/archive`) drives a laredo engine from a static
[snapshotter archive](./snapshot-writer.md) on disk — a base snapshot plus a
chain of diffs, indexed by a manifest — instead of connecting to a live database.

It exists for three workflows:

- **Offline backup / snapshot** — capture a table to a portable file and bring it
  back later.
- **Immediate startup with no PostgreSQL** — an engine comes up from the file and
  never dials a database.
- **Seeding local development** — commit a small archive and point a dev engine at
  it.

It reuses the snapshotter's versioned binary archive format wholesale — there is
no new file format. The producer side is [`laredo archive export`](#producing-an-archive)
(and the [snapshotter](./snapshot-writer.md) for continuous archives); the
consumer side is this source. See [EDR-0006](/edr/0006-archive-source) for the
design rationale.

## How it works

One archive source instance serves **one table** — a snapshotter manifest is
per-table. Configure one source per table you want to replay.

- **Baseline.** On startup the source reconstructs the table's full current state
  at the archive head (folding the base snapshot and its diffs) and hands those
  rows to the engine as the baseline. It does **not** replay historical diffs as
  change events — targets receive the current state directly.
- **Streaming.** After the baseline, the source replays any diffs recorded *after*
  the head as change events (inserts, updates, deletes, truncates).
- **Follow.** With `follow = true`, the source keeps polling the manifest for
  newly appended diffs and emits them as they arrive.
- **Replacement.** If the archive is replaced wholesale — a fresh base snapshot
  whose position no longer continues the consumer's position (a re-export, a new
  epoch, or a pruned chain) — the source signals the engine to **re-baseline**
  against the new archive. This is the same mechanism PostgreSQL uses when its
  replication slot becomes invalid, so no data is lost or double-applied.

## Configuration

```hocon
sources {
  seed {
    type = archive                 # "archive" or the alias "file"
    store = local                  # local | s3
    store_config { path = "/var/lib/laredo/archive/events" }
    format = jsonl                 # jsonl | protobuf (or a list, tried in order)
    key_prefix = "public.events/"  # MUST match the archive's write prefix
    key_fields = [id]              # primary key the archive was written with
    follow = true                  # watch for appended diffs + replacement
    poll_interval = 5s             # manifest re-read cadence while following
    state_path = "/var/lib/laredo/archive/events.ack"  # enable resume
  }
}

tables = [
  {
    source = seed
    schema = public
    table  = events
    targets = [{ type = indexed-memory }]
  }
]
```

| Option | Default | Meaning |
|---|---|---|
| `store` | — (**required**) | Destination backend: `local` or `s3` |
| `store_config.path` | — | Local filesystem root (`store = local`) |
| `store_config.bucket` / `prefix` / `region` | — | S3 destination (`store = s3`, ambient AWS credentials) |
| `format` | `jsonl` | Artifact codec(s): `jsonl`, `protobuf`, or a list tried in order |
| `key_prefix` | — | Per-table object-key prefix; **must match** the prefix the archive was written under |
| `key_fields` | `[id]` | Primary-key columns the archive was written with |
| `follow` | `false` | Keep watching for appended diffs and wholesale replacement instead of ending at the head |
| `poll_interval` | `5s` | How often the manifest is re-read while following |
| `state_path` | — | Persist the last ACKed position to this file so a restart resumes instead of re-baselining |

The schema/table come from the pipeline that binds the source (the `tables` entry
above), so you do not repeat them in the source block.

### Multiple tables (`group`)

Because one archive source serves one table, replaying several tables would mean
several near-identical source blocks. Set `group = true` to expand **one** block
into one source per table that references it, deriving each table's `key_prefix`
from `<schema>.<table>/`:

```hocon
sources {
  seed {
    type = archive
    group = true
    store = local
    store_config { path = "/var/lib/laredo/archive" }   # the archive ROOT
    format = jsonl
    follow = true
  }
}
tables = [
  { source = seed, schema = public, table = events, targets = [{ type = indexed-memory }] }
  { source = seed, schema = public, table = users,  targets = [{ type = indexed-memory }] }
]
```

This registers two sources, `seed/public.events` and `seed/public.users`, each
reading its table's prefix under the shared root. Omit `key_prefix` on a group
block (it is derived); if `state_path` is set it is treated as a **directory**
holding one `<schema>.<table>.pos` file per table. The synthesized per-table ids
(`seed/public.events`) are the handles you use with `laredo pause`/`resume`/`reload`.

### Resume vs. re-baseline

`state_path` controls whether the source resumes across restarts:

- **With `state_path`** — the last ACKed position is persisted; on restart the
  source continues from it, replaying only the diffs it missed. Use this for
  durable targets (e.g. an external database) that survive the restart.
- **Without `state_path`** — the source re-baselines on every start (folding the
  archive head afresh). This is the safe default for non-durable in-memory
  targets, which come up empty and must be repopulated. It mirrors the PostgreSQL
  source's ephemeral vs. stateful distinction (see [Sources](../concepts/sources.md#ephemeral-vs-stateful)).

## Producing an archive

Export a table's current state from PostgreSQL into a **one-shot** archive:

```bash
laredo archive export --connection "postgresql://localhost/app" \
  --schema public --table events \
  --store local --path /var/lib/laredo/archive/events --format jsonl
```

This writes a base snapshot and a manifest (recording the schema) under
`<schema>.<table>/` by default. See [`laredo archive export`](../reference/cli.md#laredo-archive-export)
for all flags.

For a **continuously updated** archive — a base snapshot plus periodic diffs,
re-basing on the snapshotter's policy — add `--follow`:

```bash
laredo archive export --connection "postgresql://localhost/app" \
  --schema public --table events \
  --store local --path /var/lib/laredo/archive/events \
  --follow --diff-interval 30s
```

`--follow` runs until interrupted (Ctrl-C), driving the [snapshotter Writer](./snapshot-writer.md)
straight from PostgreSQL (via the `snapshotter/sourcesub` adapter) rather than
from a fan-out target — so you get the snapshotter's live archive without running
a fan-out. A `follow` archive source pointed at the same location picks up the
diffs, and re-baselines whenever `--follow` re-bases.

Both export forms carry column definitions in the manifest, so schema survives the
round-trip. Archives written by older tooling (without a recorded schema) still
work — the source infers column names from a snapshot row, with types left unset.

## Library usage

The source takes a pre-built `snapshotter.Reader`, so it depends on no config or
destination wiring:

```go
import (
	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
	"github.com/zourzouvillys/laredo/source/archive"
)

reader, _ := snapshotter.NewReader(local.New("/var/lib/laredo/archive/events"),
	"public.events/", jsonl.New())

src := archive.New(
	archive.WithReader(reader),
	archive.Table("public", "events"),
	archive.KeyFields("id"),
	archive.Follow(true),
	archive.StatePath("/var/lib/laredo/archive/events.ack"),
)

eng, _ := laredo.NewEngine(
	laredo.WithSource("seed", src),
	laredo.WithPipeline(/* ... */),
)
```

## Operations

- **Position and lag.** The source reports `GetLag` as the age of the archive
  head's timestamp — a static archive's lag grows with wall-clock time, which is
  expected. `State()` moves through connected → streaming as usual.
- **Re-baseline metric.** Each wholesale replacement (or any source re-baseline)
  increments `laredo_source_rebaseline_total{source}` (Prometheus) /
  `laredo.source.rebaseline` (OTel). A rising counter on a `follow` source means
  the archive is being replaced repeatedly — watch it if you expect a static seed.
- **Refreshing a seed.** Re-run `laredo archive export` to overwrite the archive.
  A `follow` source picks up the replacement and re-baselines; a one-shot source
  (or a restarted engine) reads the new archive on next start.
- **Storage.** Local archives are plain files under `key_prefix`; back them up or
  commit small ones to a dev repo. S3 archives use the ambient AWS credential
  chain.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `load manifest: manifest not found` at startup | `key_prefix` does not match the prefix the archive was written under, or `store_config.path` is wrong | Set `key_prefix` to exactly the export/snapshotter prefix (e.g. `public.events/`); verify `manifest.json` exists under it |
| Baseline comes up empty | The archive has no reachable base snapshot, or (with `follow`) data has not been written yet | Confirm the archive was exported; with `follow = true` the source re-baselines once a snapshot appears |
| Columns have empty types | The archive was written without a recorded schema (older tooling) | Re-export with `laredo archive export`, which records the schema in the manifest |
| Engine re-baselines repeatedly while following | The archive is being re-based/replaced faster than the consumer catches up, or `key_fields` do not match how the archive was written | Match `key_fields` to the writer; widen `poll_interval`; for a static seed, set `follow = false` |
| `archive source requires a store configuration` | The source block omits `store` | Add `store = local` (with `store_config.path`) or `store = s3` |

## See also

- [Snapshot Writer](./snapshot-writer.md) — the producer of continuous archives
- [Sources](../concepts/sources.md) — the `SyncSource` contract
- [`laredo archive export` / `reconstruct`](../reference/cli.md#laredo-archive-export) — the archive CLI family
- [EDR-0006 — Archive as a SyncSource](/edr/0006-archive-source)
