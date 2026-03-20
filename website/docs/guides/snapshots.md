---
sidebar_position: 6
title: Snapshots
---

# Snapshot Management

Snapshots serialize target state for fast restart without full re-baseline.

## Configuration

```hocon
snapshot {
  enabled = true
  store = s3
  store_config {
    bucket = my-bucket
    prefix = "snapshots/"
    region = us-east-1
  }
  serializer = jsonl
  schedule = "every 6h"
  on_shutdown = true
  retention {
    keep_count = 10
    max_age = 7d
  }
  user_meta {
    config_version = "2.1"
  }
}
```

## CLI commands

```bash
# Create a snapshot
laredo snapshot create --meta config_version=2.1

# List snapshots
laredo snapshot list

# Inspect a snapshot
laredo snapshot inspect snap_20260320_123456

# Restore from a snapshot
laredo snapshot restore snap_20260320_123456

# Delete a snapshot
laredo snapshot delete snap_20260319_183000

# Prune old snapshots (keep N most recent)
laredo snapshot prune --keep 3
```

## Library usage

```go
engine, _ := laredo.NewEngine(
    // ... sources and pipelines ...
    laredo.WithSnapshotStore(s3.New(
        s3.Bucket("my-bucket"),
        s3.Prefix("snapshots/"),
    )),
    laredo.WithSnapshotSerializer(jsonl.New()),
    laredo.WithSnapshotSchedule(6 * time.Hour),
    laredo.WithSnapshotOnShutdown(true),
)
```

## Retention

After each snapshot, the engine enforces retention by deleting snapshots that exceed `keep_count` or `max_age`. Retention is also enforced on the `prune` CLI command and gRPC call.
