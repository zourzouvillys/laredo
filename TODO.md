# Laredo — Remaining Work

Items not yet implemented. Everything else from the original v1.0 roadmap has been completed and shipped in v0.1.0.

---

## PostgreSQL Source

- [x] Sync publication on startup (add/remove tables, update publish operations)
- [x] Row filters and column lists (PostgreSQL 15+)
- [x] Ephemeral mode reconnect: signal engine for full re-baseline

## Fan-Out Replication Protocol

- [x] Delta-from-snapshot mode: tell client to use local snapshot, send journal delta → live
- [x] Atomic handoff: pin journal during snapshot send, no gaps
- [x] Cross-instance failover: resume by source position (WAL LSN), not per-instance
      sequence. Journal entries carry `source_position`; `Sync` accepts
      `last_known_source_position` and replies with a delta.
- [x] Graceful drain: server sends `GoAway` (on `SIGTERM` via `--drain-grace`, or
      the OAM `DrainReplication` admin RPC); client overlaps the cutover and
      disconnects the old instance cleanly.

### Deferred

- [ ] Wire the fan-out target + replication service into the pre-built
      `laredo-server` from HOCON config (a `replication-fanout` target type and
      mounting `LaredoReplicationService`). Today these are library components;
      the SIGTERM/OAM drain hooks are in place but only act on fan-out targets
      once an engine is built with them (e.g. via a custom `main`). Until then,
      `--drain-grace`/`DrainReplication` are no-ops on the stock binary.
- [ ] Add `source_position` to `SnapshotBegin`/`SnapshotEnd` so a client that
      fails over immediately after a full snapshot (before any journal entry)
      can still resume by position instead of re-snapshotting.
