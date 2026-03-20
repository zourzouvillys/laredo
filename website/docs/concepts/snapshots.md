---
sidebar_position: 5
title: Snapshots
---

# Snapshots

The engine can serialize the current state of any target at a point-in-time, tagged with the source position, and write it to a pluggable snapshot store. On startup, an instance can restore from a snapshot instead of doing a full baseline.

## Startup paths

| Path | When | What happens |
|---|---|---|
| **Cold start** | No snapshot, no prior position | Full baseline from source |
| **Resume** | Source supports resume, has ACKed position | Skip baseline, resume stream |
| **Snapshot restore** | Snapshot exists, positions still valid | Load snapshot, resume from snapshot position |

## Startup flow

```
on startup:
    1. Check snapshot store for latest snapshot
    2. If snapshot exists:
        a. Load metadata, get source positions
        b. Check if each source can stream from that position
        c. If yes → restore_snapshot() + resume streaming
        d. If no → discard snapshot, fall through
    3. Full baseline from source
```

## Snapshot creation

Snapshots can be triggered:

- **Manually** via gRPC or CLI (`laredo snapshot create`)
- **Periodically** on a configurable interval
- **On shutdown** for fast restart

The creation flow:

1. Pause all sources
2. Record current position for each source
3. Export each target's state through the snapshot serializer
4. Write to snapshot store
5. Resume all sources

## Snapshot stores

| Store | Module | Use case |
|---|---|---|
| Local disk | `snapshot/local` | Development, single-node |
| S3 | `snapshot/s3` | Production, shared storage |

## Snapshot serializers

| Format | Module | Description |
|---|---|---|
| JSONL | `snapshot/jsonl` | Newline-delimited JSON (default) |

## User metadata

Snapshots carry an opaque user metadata map. Use it for config fingerprints, version info, or compatibility checks:

```go
engine.CreateSnapshot(ctx, map[string]any{
    "config_version": "2.1",
    "service_id":     "warden-01",
})
```

On restore, inspect the metadata before committing:

```go
descriptor, _ := store.Describe(ctx, snapshotID)
if !isCompatible(descriptor.UserMeta) {
    // reject, fall back to full baseline
}
```
