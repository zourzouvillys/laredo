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

### Subscription filtering — follow-ups

Server-side per-subscription filtering shipped (`SyncRequest.filters`,
`equals`/`prefix`/`in`, applied across snapshot + catch-up + live). Deferred:

- [ ] Advance a filtered client's resume cursor during long matching-silence
      (e.g. carry an advanced position on heartbeats and let the client
      checkpoint it) so a sparse partition does not force a re-snapshot after a
      reconnect once the journal prunes past the last *received* entry.
- [ ] Richer predicate types if a use case needs them: numeric/time range
      comparisons, negation, or a full CEL expression. Kept out of the first cut
      deliberately — `equals`/`prefix`/`in` cover partition scoping.
- [ ] Surface per-client filter state in `GetReplicationStatus` /
      `ConnectedClient` (e.g. whether a client is filtered) for operability.

### Cold-tier replay (EDR-0002)

Design landed: [EDR-0002](docs/edr/0002-cold-tier-replay.md) — serve too-stale
fan-out clients from the snapshotter's cold archive (`SYNC_MODE_REPLAY_ARCHIVE`)
instead of a full live re-snapshot. Implementation:

- [ ] `snapshotter.Reader` — `LoadManifest`, artifact decode (reuse `Format`),
      and `Plan(manifest, fromPosition, cmp)` chain selection (diff-only /
      snapshot-base / none).
- [ ] Export `ManifestObjectKey` / `ArtifactObjectKey` (refactor the writer's
      private key logic; no write-side behaviour change).
- [ ] `SYNC_MODE_REPLAY_ARCHIVE` in the proto + the replication cold-replay path:
      hot-journal pin, gapless position-based handoff, fall back to
      `FULL_SNAPSHOT` on any cold-path failure.
- [ ] Fan-out target `archive` config (read-only destination + formats).
- [ ] Tests: reader unit (diff-only, snapshot-base, gap, unknown version,
      missing artifact) + cold-replay integration; docs + runbook.
