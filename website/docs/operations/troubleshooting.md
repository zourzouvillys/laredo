---
sidebar_position: 4
title: Troubleshooting
---

# Troubleshooting

Common issues and how to resolve them.

## Pipeline stuck in BASELINING

**Symptom**: Pipeline state stays in `BASELINING` for a long time.

**Causes**:
- Large table with millions of rows
- Slow network to PostgreSQL
- PostgreSQL under heavy load

**Solutions**:
- Check `laredo status` for row count progress
- Increase the startup probe `failureThreshold` in Kubernetes
- Consider adding publication-level column lists to reduce data volume

## Pipeline in ERROR state

**Symptom**: `laredo status` shows a pipeline in ERROR.

**Steps**:
1. Check the error message: `laredo status --table <table>`
2. Check dead letters: `laredo dead-letters <pipeline_id>`
3. Fix the underlying issue (e.g., downstream HTTP service down)
4. Reload to restart the pipeline: `laredo reload <table>`

## Replication slot not found

**Symptom**: `ERROR: replication slot "laredo_01" does not exist`

**Causes**:
- Slot was dropped manually
- Slot was invalidated by PostgreSQL (`max_slot_wal_keep_size` exceeded)
- First startup in stateful mode (expected — slot will be created)

**Solution**: Laredo auto-creates the slot on startup in stateful mode. If it was dropped, restart Laredo for a full re-baseline.

## Permission denied on replication

**Symptom**: `ERROR: must be superuser or replication role to use replication slots`

**Solution**: Grant the replication privilege:
```sql
ALTER USER laredo_user REPLICATION;
```

For publication management (`create = true`), also grant:
```sql
GRANT CREATE ON DATABASE mydb TO laredo_user;
```

## Fan-out clients disconnecting

**Symptom**: Clients repeatedly reconnect and re-snapshot.

**Causes**:
- Client falling behind the journal (journal entries pruned before client catches up)
- Network instability
- Client-side processing too slow

**Solutions**:
- Increase `journal.max_entries` and `journal.max_age`
- Check `laredo fanout clients <table>` for client lag
- Increase `client_buffer.max_size` if using `drop_disconnect` policy
- Use `LocalSnapshotPath` in the client for faster reconnects

## High memory usage

**Symptom**: Laredo process consuming more memory than expected.

**Causes**:
- Large tables in in-memory targets
- Large change journal in fan-out targets
- Many connected fan-out clients with deep buffers

**Solutions**:
- Use publication-level column lists to exclude large columns
- Use pipeline filters to exclude unnecessary rows
- Tune `journal.max_entries` for fan-out targets
- Monitor `laredo_pipeline_row_count` and `laredo_fanout_journal_entries`
