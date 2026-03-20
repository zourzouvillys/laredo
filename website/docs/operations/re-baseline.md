---
sidebar_position: 2
title: Re-Baseline
---

# Forcing a Re-Baseline

A re-baseline reloads all rows from the source for a table, replacing the target's current state.

## When to re-baseline

- After a suspected data drift
- After a schema change that a target can't handle incrementally
- After restoring a database backup
- After dropping and recreating a replication slot

## Trigger via CLI

```bash
# Re-baseline a specific table
laredo reload public.config_document

# Re-baseline all tables on a source
laredo reload --source pg_main --all

# Re-baseline everything
laredo reload --all
```

## What happens during re-baseline

1. Engine **pauses** the change stream (stops reading, keeps connection open)
2. Engine calls `OnTruncate` on all targets for the table (clears existing state)
3. Opens a new `REPEATABLE READ` snapshot transaction
4. Executes `SELECT *` for each table, delivers rows via `OnBaselineRow`
5. Calls `OnBaselineComplete`
6. **Resumes** the change stream
7. Applies any changes that accumulated during the reload
8. ACKs the current position once reload and catch-up are complete

Some changes may overlap with rows just loaded during the reload window. Targets must be idempotent for this brief overlap.

## Resetting a source

For a full reset (drops and recreates the replication slot):

```bash
laredo reset-source pg_main
```

This is more drastic than a reload — it destroys all position tracking. Use it when the slot is corrupted or you're changing the publication configuration.

To also drop the publication (for decommissioning):

```bash
laredo reset-source pg_main --drop-publication
```
