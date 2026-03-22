---
sidebar_position: 6
title: Reset Source
---

# Resetting a Source

Resetting a source drops the replication slot (and optionally the publication), clears all position tracking, and forces a full re-baseline on the next startup cycle. This is more disruptive than a reload — it destroys all replication state.

## When to reset

| Scenario | Why reset is needed |
|---|---|
| Slot invalidation | WAL data is gone; the slot cannot resume |
| Slot corruption | PostgreSQL reports the slot is in an inconsistent state |
| Changing publication tables | Adding or removing tables from the publication (with `create = true`, Laredo handles this automatically; reset is for manual publication management) |
| Decommissioning a source | Clean up the slot and publication from PostgreSQL |
| Switching from ephemeral to stateful mode | Old slot configuration is incompatible |
| Changing the slot name | Old slot must be dropped manually or via reset |

## Reset via CLI

### Basic reset (slot only)

```bash
laredo reset-source pg_main
```

This prompts for confirmation:

```
This will reset source "pg_main" (drop and recreate replication slot).
Continue? [y/N] y
```

The command drops the replication slot named in the source configuration. The next engine startup cycle creates a new slot and runs a full baseline.

### Reset with publication drop

```bash
laredo reset-source pg_main --drop-publication
```

This additionally drops the PostgreSQL publication. Use this when decommissioning a source entirely, or when you need to rebuild the publication from scratch.

## Reset via gRPC

The OAM gRPC service exposes the `ResetSource` RPC:

```protobuf
rpc ResetSource(ResetSourceRequest) returns (ResetSourceResponse);
```

```json
{
  "source_id": "pg_main",
  "confirm": true,
  "drop_publication": false
}
```

The `confirm` field must be `true` — the server rejects requests without confirmation as a safety measure.

## What happens during a reset

1. **Slot drop**: Laredo executes `DROP REPLICATION SLOT` on the PostgreSQL replication connection. If the slot does not exist (already dropped), this step is skipped.

2. **Publication drop** (if `--drop-publication`): Laredo executes `DROP PUBLICATION IF EXISTS` for the publication associated with the source.

3. **Position cleared**: The engine clears its internal position tracking (`slotLSN` and `slotExisted` flags). The source no longer has a resume point.

4. **Re-baseline on next cycle**: When the engine restarts the source (either because it was restarted or via the internal restart loop), it creates a new replication slot and performs a full baseline for all tables on that source.

All targets bound to the reset source receive `OnTruncate` followed by the full baseline row set.

## Verifying the reset

After the reset completes, check that the source recovered:

```bash
# Source should show a new slot and be streaming
laredo source pg_main

# Pipelines should transition through BASELINE → STREAMING
laredo pipelines
```

Verify in PostgreSQL that the old slot is gone and the new one exists:

```sql
SELECT slot_name, active, restart_lsn, confirmed_flush_lsn
FROM pg_replication_slots
WHERE slot_name LIKE 'laredo%';
```

## Safety considerations

- **Reset is destructive.** All position tracking is lost. The next baseline reads every row from every table on the source. For large tables, this can take significant time and put load on PostgreSQL.
- **Downstream targets are cleared.** All targets receive a truncate event before the new baseline rows arrive. During this window, queries against in-memory targets return empty results.
- **The CLI requires confirmation.** The `y/N` prompt prevents accidental resets. The gRPC API requires `confirm = true`.
- **Only resettable sources support this.** The source must implement the `Resettable` interface. The PostgreSQL source (`source/pg`) supports it. Other sources may not.

## Decommissioning a source

To fully remove a source from PostgreSQL:

```bash
# Drop the slot and publication
laredo reset-source pg_main --drop-publication

# Then remove the source from the laredo-server config and restart
```

If Laredo is not running, you can drop the slot and publication directly in PostgreSQL:

```sql
SELECT pg_drop_replication_slot('laredo_01');
DROP PUBLICATION IF EXISTS laredo_01_pub;
```
