# source/pg

PostgreSQL logical replication source using the built-in `pgoutput` plugin.

## Design Rules

- **Two connections**: one for replication streaming, one for queries (baseline SELECT, pg_catalog, health checks). Never mix them.
- **Ephemeral mode**: temporary replication slot, auto-dropped on disconnect. Full baseline every startup. `SupportsResume()` returns `false`.
- **Stateful mode**: persistent named slot. Resume from last ACKed LSN. `SupportsResume()` returns `true`.
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
