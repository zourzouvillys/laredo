---
sidebar_position: 7
title: Snapshot Restore
---

# Restoring from a Snapshot

Snapshots capture point-in-time state of all pipeline targets along with source positions. After data loss, corruption, or a failed migration, you can restore from a snapshot to avoid a full re-baseline from the source database.

## When to use snapshot restore

| Scenario | Why restore helps |
|---|---|
| Process crash without graceful shutdown | Targets lost in-memory state; snapshot avoids full re-baseline |
| Corrupted target state | Replace bad data with known-good snapshot |
| Rolling back a bad schema change | Restore to pre-change snapshot, then replay |
| Disaster recovery | Rebuild from the most recent snapshot on a fresh instance |
| Testing / staging refresh | Load production snapshot into a test environment |

Snapshot restore is faster than a full re-baseline because it reads from the snapshot store (local disk or S3) rather than scanning every row in the source database.

## Prerequisites

Snapshot restore requires:

- A **snapshot store** configured (`local` or `s3`)
- A **snapshot serializer** configured (`jsonl`)
- At least one previously created snapshot

```hocon
snapshot {
  store = local
  config {
    directory = "/var/lib/laredo/snapshots"
  }
  serializer = jsonl
  on_shutdown = true
}
```

## Listing snapshots

```bash
laredo snapshot list
```

Output:

```
SNAPSHOT ID                     CREATED                  TABLES  ROWS    FORMAT
snap-1710950400000000000        2026-03-20 16:00:00      3       12847   jsonl
snap-1710946800000000000        2026-03-20 15:00:00      3       12831   jsonl
snap-1710943200000000000        2026-03-20 14:00:00      3       12815   jsonl
```

Filter by table:

```bash
laredo snapshot list --table public.config_document
```

## Inspecting a snapshot

Before restoring, inspect the snapshot to verify it contains the expected data:

```bash
laredo snapshot inspect snap-1710950400000000000
```

Output includes:

- Snapshot ID and creation timestamp
- Source positions at the time of the snapshot
- Table list with row counts and column definitions
- User metadata (trigger reason, custom tags)
- Storage format

Check that the row counts look reasonable and that the source positions are recent enough for your recovery needs.

## Restoring via CLI

```bash
laredo snapshot restore snap-1710950400000000000
```

The CLI prompts for confirmation:

```
This will restore snapshot "snap-1710950400000000000", replacing current pipeline state.
Continue? [y/N] y
```

On confirmation, the engine:

1. Pauses all sources
2. Loads the snapshot from the store
3. Calls `RestoreSnapshot` on each target with the snapshot's row data
4. Resumes streaming from the source position recorded in the snapshot
5. Applies any changes that occurred between the snapshot and the current WAL position

## Restoring via gRPC

The OAM gRPC service exposes `RestoreSnapshot`:

```protobuf
rpc RestoreSnapshot(RestoreSnapshotRequest) returns (RestoreSnapshotResponse);
```

```json
{
  "snapshot_id": "snap-1710950400000000000",
  "confirm": true
}
```

## Verification steps

After the restore completes, verify that the data is correct:

### 1. Check pipeline states

```bash
laredo pipelines
```

All pipelines should show STREAMING state after restore and catch-up complete.

### 2. Verify row counts

```bash
laredo query count public.config_document
laredo query count public.user_preferences
```

Compare against the row counts shown in `laredo snapshot inspect`.

### 3. Spot-check data

```bash
laredo query get public.config_document --key "doc-001"
```

Verify that a known row has the expected values.

### 4. Check source lag

```bash
laredo source pg_main
```

After restore, the source catches up from the snapshot's position to the current WAL position. Lag should decrease to near zero once catch-up completes.

### 5. Monitor for errors

Watch the Laredo logs for any errors during the catch-up phase. Changes that arrived after the snapshot was taken are replayed from the WAL, and any issues will appear in the log output.

## Snapshot management

### Creating snapshots manually

```bash
laredo snapshot create
```

### Automatic snapshots

Configure periodic snapshots and shutdown snapshots:

```hocon
snapshot {
  schedule = 1h         # take a snapshot every hour
  on_shutdown = true     # take a snapshot on graceful shutdown
  retention = 10         # keep the 10 most recent snapshots
  store = local
  config {
    directory = "/var/lib/laredo/snapshots"
  }
  serializer = jsonl
}
```

### Pruning old snapshots

```bash
laredo snapshot prune --keep 5
```

This deletes all but the 5 most recent snapshots.

### Deleting a specific snapshot

```bash
laredo snapshot delete snap-1710943200000000000
```

## Automatic snapshot restore on startup

When a snapshot store is configured and Laredo starts, the engine automatically attempts to restore from the latest snapshot before falling back to a full baseline. The logic is:

1. List snapshots (newest first)
2. Load the latest snapshot
3. Check that the snapshot contains a source position for each configured source
4. Restore each target from the snapshot data
5. Resume streaming from the snapshot's source position

If any step fails (no snapshots, missing source position, target restore error), the engine falls through to a full baseline. No manual intervention is needed.

## Troubleshooting

| Problem | Cause | Solution |
|---|---|---|
| "no snapshot store configured" | Snapshot store not in config | Add `snapshot.store` to the configuration |
| "snapshot has no position for source X" | Snapshot was taken before the source was added | Use a newer snapshot or fall back to full baseline |
| Restore succeeds but data looks stale | Snapshot is old and catch-up from WAL is incomplete | Wait for catch-up to finish; check lag with `laredo source` |
| Restore fails with target error | Target cannot process snapshot entries | Check logs for the specific error; the engine will fall back to full baseline |
