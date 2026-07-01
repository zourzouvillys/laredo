# source/pg

PostgreSQL logical replication source using the built-in `pgoutput` plugin.

## Design Rules

- **Two connections**: one for replication streaming, one for queries (baseline SELECT, pg_catalog, health checks). Never mix them.
- **Consistent baseline handoff**: create the slot with `EXPORT_SNAPSHOT` and import that snapshot in the baseline transaction (`SET TRANSACTION SNAPSHOT`), then stream from the slot's consistent point. The COPY reads at exactly the slot's consistent point — no gap, no duplicates (matches native `copy_data = true`). The exported snapshot is single-use and only valid before streaming starts, so the baseline must run before `StartReplication` and the replication connection must stay idle in between. `Baseline` consumes the snapshot name; a mid-stream `Reload` falls back to a plain snapshot anchored to `pg_current_wal_lsn()`.
- **Ephemeral mode**: temporary replication slot, auto-dropped on disconnect. Full baseline every startup. `SupportsResume()` returns `false`.
- **Stateful mode**: persistent named slot. Resume from last ACKed LSN. `SupportsResume()` returns `true`.
- **AlwaysBaseline**: forces a full baseline every startup even in stateful mode by returning `nil` from `LastAckedPosition()` (slot stays persistent, `SupportsResume()` stays `true`). For non-durable targets (in-memory) that would otherwise resume into an empty target. Streaming resumes from the reused slot's confirmed LSN, so the overlap is re-delivered (idempotent), never lost.
- **Publication management**: when `create = true`, auto-create and keep the publication in sync with configured tables. Support PG 15+ row filters and column lists.
- **Reconnect internally**: on transient connection loss, attempt reconnect with exponential backoff before surfacing error to the engine. State machine: CONNECTING → CONNECTED → STREAMING → RECONNECTING → ERROR.
- **Position type**: LSN (`uint64` with string format `"0/XXXXXXXX"`).

## Testing

- Unit tests: LSN parsing/formatting, position comparison, config validation.
- Integration tests (in `test/integration/`): require real PostgreSQL via testcontainers.
  - Ephemeral mode: full baseline + streaming.
  - Stateful mode: baseline, stream, restart, resume from LSN.
  - Publication management: auto-create, add/remove tables.
  - Reconnect: simulate connection loss, verify resume.
  - Schema changes: column add/drop detection from replication stream.
