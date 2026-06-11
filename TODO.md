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

### Server wiring (ADR-007)

Shipped: ADR-007 (amends ADR-005) — the stock `laredo-server` serves a fan-out
from HOCON. A `replication-fanout` target type maps onto `target/fanout.New`
options, and the binary mounts a single engine-global `LaredoReplicationService`
(routing by table) on a dedicated listener (default `4002`) via
`service.EnableReplication` whenever a fan-out target is configured.

- [x] `replication-fanout` config target type (`journal` / `snapshot` retention /
      `client_buffer` / `max_clients` / `heartbeat_interval`).
- [x] Top-level `fanout { grpc { port } }` listener block (engine-global).
- [x] `service.EnableReplication`; mount + graceful shutdown in `laredo-server`.
      `--drain-grace`/`DrainReplication` now act on the stock binary's fan-out
      targets.

Deferred:

- [ ] Durable on-disk fan-out snapshot store from config (`snapshot { store;
      store_config; serializer }`). Today `target/fanout` snapshots are in-memory
      only, so that block has no backing option and is omitted from the config
      contract; add it when a persistent-snapshot option lands on the target.
- [ ] Wire the cold-tier archive reader (`replication.WithArchive`) from HOCON now
      that the target is config-wired — needs an archive destination/format config
      home (shared with the EDR-0003 CLI item below).
- [ ] Optional: co-mount replication on the OAM/Query port, or run multiple
      replication listeners, for operators who want a single port or finer
      isolation. No contract change required.

### Deferred (protocol)

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

Shipped: [EDR-0002](docs/edr/0002-cold-tier-replay.md) — serve too-stale fan-out
clients from the snapshotter's cold archive (`SYNC_MODE_REPLAY_ARCHIVE`) instead
of a full live re-snapshot.

- [x] `snapshotter.Reader` — `LoadManifest`, artifact decode (reuse `Format`),
      and `Plan(manifest, fromPosition, cmp)` chain selection (diff-only /
      snapshot-base / none).
- [x] Export `ManifestObjectKey` / `ArtifactObjectKey` (refactor the writer's
      private key logic; no write-side behaviour change).
- [x] `SYNC_MODE_REPLAY_ARCHIVE` in the proto + the replication cold-replay path:
      hot-journal pin, gapless position-based handoff, fall back to
      `FULL_SNAPSHOT` on any cold-path failure.
- [x] Archive wired via `replication.WithArchive` (per table). Placed on the
      replication service, not the fan-out target, to keep the core target from
      depending on the snapshotter.
- [x] Tests: reader unit (diff-only, snapshot-base, gap, unknown version,
      missing artifact) + cold-replay integration; docs.

Deferred:

- [ ] Wire the archive from HOCON — now unblocked (the fan-out target is
      config-wired per ADR-007); tracked under "Server wiring → Deferred" above.
- [ ] Cold-replay operations runbook (fleet-reconnect read amplification on the
      archive, monitoring).
- [ ] Diff-only resume when the client's position aligns to a diff boundary is
      implemented; consider serving partial-diff ranges for non-aligned positions
      if a use case needs to avoid the base-snapshot re-read.

### Point-in-time reconstruction (EDR-0003)

Shipped: [EDR-0003](docs/edr/0003-point-in-time-reconstruction.md) —
`Reader.ReconstructAsOf` / `PlanAsOf` materialize a table's state as of any
source position from the archive (library-only).

Deferred:

- [ ] Expose reconstruction through a surface: a `laredo` CLI subcommand
      (`archive reconstruct --at <position>`) and/or a read RPC, once archive
      destination/format config has a config home.
- [ ] Sub-diff (intra-range) precision would need per-change positions in the
      diff format; out of scope until a use case requires landing between
      artifact boundaries.

### Cascading fan-out source (EDR-0004)

Shipped: [EDR-0004](docs/edr/0004-cascading-fanout-source.md) — `source/fanout`,
a `SyncSource` that consumes an upstream fan-out so engines can cascade.

- [x] Expose the handshake `columns` on `client/fanout` (`Columns()`) and
      populate them in the replication handshake.
- [x] `ListenWithPosition` on `client/fanout` (per-change source position).
- [x] `source/fanout` implementing `SyncSource` over the client (Baseline from
      `All()`, Stream from `ListenWithPosition`, resume/ack by source position).
- [x] Position comparator: default PostgreSQL-LSN order, `WithPositionComparator`
      override.
- [x] Integration test cascading two engines (baseline + live propagation); docs.

Deferred:

- [ ] True `Pause` (stop the upstream stream while paused, not just a state flip).
- [ ] Resume-after-downstream-restart test (the client re-snapshots; the source
      re-baselines) and lag reporting via `GetLag`.
- [ ] Topology helpers: loop detection / depth limits for multi-hop cascades.
