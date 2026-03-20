---
sidebar_position: 1
title: Slot Lag
---

# Handling Slot Lag

PostgreSQL replication slots retain WAL segments until the consumer acknowledges them. If the consumer falls behind, WAL accumulates and disk usage grows.

## Monitoring lag

```bash
# Via CLI
laredo source pg_main
# Shows: Lag: 1.2 KB

# Via Prometheus
# laredo_source_lag_bytes{source_id="pg_main"}
```

## Common causes

| Cause | Solution |
|---|---|
| Slow target (HTTP 503, batch timeouts) | Fix downstream, or increase retry limits |
| Laredo paused | Resume: `laredo resume` |
| Large transaction blocking ACK | Check for long-running transactions in PostgreSQL |
| Process crashed | Restart — stateful mode resumes from last ACK |

## Slot invalidation

If the slot falls too far behind (`max_slot_wal_keep_size` exceeded), PostgreSQL invalidates it. Laredo detects this, drops the invalidated slot, and performs a full re-baseline automatically.

To avoid invalidation:

- Monitor `laredo_source_lag_bytes` and alert before it reaches `max_slot_wal_keep_size`
- Keep targets healthy to avoid ACK delays
- Use `snapshot.on_shutdown = true` so restarts don't require a full re-baseline

## Emergency: WAL disk full

If WAL is consuming too much disk:

```bash
# Check slot status in PostgreSQL
SELECT slot_name, active, restart_lsn, confirmed_flush_lsn,
       pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) AS lag_bytes
FROM pg_replication_slots;

# If Laredo is down and you need to free WAL immediately:
SELECT pg_drop_replication_slot('laredo_01');
```

After dropping the slot, Laredo will perform a full re-baseline on next startup.
