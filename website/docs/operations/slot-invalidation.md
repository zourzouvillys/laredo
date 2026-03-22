---
sidebar_position: 5
title: Slot Invalidation
---

# Recovering from Slot Invalidation

PostgreSQL can invalidate a replication slot when the server needs to remove WAL segments that the slot still references. Once invalidated, the slot can no longer be used for replication. Laredo must reset the source and perform a full re-baseline.

## What is slot invalidation

PostgreSQL retains WAL segments as long as a replication slot needs them. When the `max_slot_wal_keep_size` server setting is configured and lag exceeds that threshold, PostgreSQL removes the WAL segments and marks the slot as invalidated. An invalidated slot cannot resume streaming — the WAL data it needs no longer exists on disk.

This is distinct from normal slot lag. With lag, the data is still available and the consumer can catch up. With invalidation, the data is gone.

## Detecting slot invalidation

### Error messages in Laredo logs

When Laredo encounters an invalidated slot during streaming, the PostgreSQL source logs an error like:

```
pg source: replication slot "laredo_01" has been invalidated; it must be dropped and recreated
```

or:

```
pg source: cannot read from logical replication slot "laredo_01", it has been invalidated
```

### Checking slot status in PostgreSQL

Query the `pg_replication_slots` view directly:

```sql
SELECT slot_name, active, wal_status, safe_wal_size
FROM pg_replication_slots
WHERE slot_name = 'laredo_01';
```

| Column | Meaning |
|---|---|
| `wal_status = 'lost'` | Slot is invalidated — WAL has been removed |
| `wal_status = 'extended'` | Slot is retaining WAL beyond `max_wal_size` but not yet invalidated |
| `wal_status = 'reserved'` | Normal operation |
| `safe_wal_size` | Bytes remaining before invalidation; NULL means unlimited |

### Monitoring

The `laredo_source_lag_bytes` Prometheus metric tracks how far behind the slot is. Alert when this approaches `max_slot_wal_keep_size`:

```yaml
# Prometheus alert rule
- alert: LaredoSlotNearInvalidation
  expr: laredo_source_lag_bytes > 0.8 * <max_slot_wal_keep_size_bytes>
  for: 5m
  annotations:
    summary: "Laredo slot lag is approaching invalidation threshold"
```

## Recovery

### Step 1: Reset the source via CLI

Use the `reset-source` command to drop the invalidated slot and recreate it:

```bash
laredo reset-source pg_main
```

This drops the replication slot and clears all position tracking. Laredo will create a new slot and perform a full baseline on the next startup cycle.

If the publication also needs to be recreated (for example, if the table set has changed):

```bash
laredo reset-source pg_main --drop-publication
```

### Step 2: Verify recovery

After the reset, Laredo performs a full re-baseline automatically. Monitor progress:

```bash
# Check source state
laredo source pg_main

# Check pipeline states
laredo pipelines
```

All pipelines on the affected source will transition through BASELINE and then STREAMING.

### Step 3: Verify data consistency

After the baseline completes, verify row counts match expectations:

```bash
laredo query count public.config_document
```

## Prevention

### Configure max_slot_wal_keep_size appropriately

In `postgresql.conf`, set a WAL retention limit that balances disk usage against your tolerance for re-baselines:

```
# Keep up to 10 GB of WAL for replication slots
max_slot_wal_keep_size = '10GB'
```

A value of `0` (default in PostgreSQL 13+) means unlimited retention — the slot will never be invalidated, but WAL can grow without bound.

### Use snapshot-on-shutdown

Configure Laredo to take a snapshot before shutting down. This reduces the amount of WAL needed on restart, since the engine can restore from the snapshot and only replay changes since the snapshot was taken:

```hocon
snapshot {
  on_shutdown = true
  store = local
  config {
    directory = "/var/lib/laredo/snapshots"
  }
}
```

### Keep targets healthy

Slot lag grows when targets are slow to acknowledge changes. Monitor target health and fix downstream issues promptly:

- HTTP sync targets returning errors slow down ACK progression
- Fan-out targets with many slow clients can apply backpressure
- Dead letters indicate persistent downstream failures

### Monitor and alert

Set up alerts on slot lag well before the invalidation threshold:

```bash
# Check lag via CLI
laredo source pg_main
# Shows: Lag: 1.2 KB
```

Use `laredo_source_lag_bytes` for automated monitoring. A sudden spike in lag often precedes invalidation if left unaddressed.

## Automatic recovery

When Laredo detects that a slot has been invalidated during streaming, it automatically drops the invalidated slot and initiates a full re-baseline. No manual intervention is required in this case — the recovery happens transparently. The source transitions through RECONNECTING and then restarts the baseline process.

Manual `reset-source` is only needed when the slot is invalidated while Laredo is stopped, or when automatic recovery fails (for example, if PostgreSQL permissions have changed).
