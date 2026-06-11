# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **snapshotter/laredo-server**: S3 cold-tier archives from HOCON (EDR-0005).
  Extracted the snapshotter binary's destination/format building into a new
  importable `snapshotter/destwire` package (`BuildDestination`, `BuildFormats`,
  `AmbientAWSConfig`); `laredo-snapshotter` now delegates to it, and
  `laredo-server` accepts `archive.store = s3` through the same path — one wiring,
  no duplication. S3 uses the ambient AWS credential chain (env / IRSA /
  instance-role); named profiles and assume-role for the server archive remain
  future work.
- **laredo-server**: Cold-tier archive from HOCON (EDR-0005). A
  `replication-fanout` target may carry an `archive { store, store_config,
  format, key_prefix }` block; `laredo-server` builds a `snapshotter.Reader` and
  registers it via `replication.WithArchive` for the target's `(schema, table)`,
  so clients that fall behind the in-memory journal replay from the cold archive
  (`SYNC_MODE_REPLAY_ARCHIVE`) instead of taking a full live re-snapshot. New
  `config.BuildArchiveReader`. `store = local` is supported (the offline-first /
  onboard case); `store = s3` is rejected with a clear error pending a shared
  destination-builder. A missing/empty archive degrades to the live path; a bad
  archive block fails startup loudly.
- **laredo-server**: Serve a fan-out straight from HOCON. A `type =
  replication-fanout` target is now a first-class config target type, and the
  stock binary mounts a single engine-global `LaredoReplicationService` (routing
  by table) on a dedicated listener — default port `4002`, separate from the
  OAM/Query port — whenever any fan-out target is configured. The target's
  `journal`, in-memory `snapshot` retention, `client_buffer`, `max_clients`, and
  `heartbeat_interval` map onto `target/fanout.New` options; a top-level
  `fanout { grpc { port } }` block overrides the listener port. New
  `service.EnableReplication` mounts the handler like `EnableOAM` / `EnableQuery`.
  See ADR-007 (amends ADR-005).
- **Fan-out replication**: Per-subscription server-side filtering. A `Sync`
  client can attach AND-combined column predicates (`equals`, `prefix`, `in`) via
  the new `SyncRequest.filters` field; the server applies them uniformly across
  the snapshot, journal catch-up, and live phases, so excluded rows never cross
  the wire. Enables partition scoping (e.g. one tenant's slice of a shared table)
  from a single fan-out target.
- **Fan-out client**: `WithFilterEquals`, `WithFilterPrefix`, and `WithFilterIn`
  options to set a subscription filter.
- **Fan-out replication**: Cold-tier replay (`SYNC_MODE_REPLAY_ARCHIVE`). A `Sync`
  client whose position predates the in-memory journal can be resumed from the
  cold archive written by `laredo-snapshotter` (base snapshot + diffs) instead of
  a full live re-snapshot, then handed off gaplessly to the hot journal and live
  stream. Register an archive per table with `replication.WithArchive`; any
  archive problem falls back to a full live snapshot before any data is sent. See
  EDR-0002.
- **Snapshotter**: `snapshotter.Reader` — the read-side inverse of `Writer`
  (`LoadManifest`, `ReadSnapshot` / `ReadDiff`, and `Plan` chain selection), plus
  exported `ManifestObjectKey` / `ArtifactObjectKey` helpers, so consumers can
  reconstruct a table from the archive.
- **Snapshotter**: Point-in-time reconstruction — `Reader.ReconstructAsOf` (and
  `PlanAsOf`) materialize a table's full state as of any source position from the
  archive (newest base snapshot + the folded diffs up to it), returning the rows
  and the effective position. Library-only; see EDR-0003.
- **Cascading replication**: `source/fanout` — a `SyncSource` that consumes an
  upstream laredo fan-out, so a downstream engine can treat a fan-out as a source
  (edge / regional trees) without each leaf holding a PostgreSQL slot. Inherits
  snapshot, resume, cold-tier replay, and failover from `client/fanout`; orders
  positions with a pluggable comparator (default PostgreSQL-LSN). See EDR-0004.
- **Fan-out client**: `Columns()` (table schema from the handshake) and
  `ListenWithPosition` (change listener that also receives the source position).
- **Fan-out replication**: the `Sync` handshake now carries the table's column
  definitions, so cascading consumers can discover the schema.

## [0.2.0] - 2026-04-14

### Added
- **PostgreSQL source**: Publication sync on startup — add/remove tables and update publish operations without drop/recreate
- **PostgreSQL source**: Row filters and column lists for publications (PostgreSQL 15+)
- **PostgreSQL source**: Ephemeral mode automatically triggers a full re-baseline on reconnect
- **PostgreSQL source**: `BeforeConnect` hook for per-connection config mutation
- **Fan-out replication**: Delta-from-snapshot sync mode — clients can use a local snapshot and receive only the journal delta until they reach live
- **Fan-out replication**: Atomic handoff — the journal is pinned during snapshot send so clients see no gaps between snapshot and live stream

### Changed
- Dependency bumps: `lodash`, `github.com/aws/aws-sdk-go-v2/service/kinesis`, `path-to-regexp`, `go.opentelemetry.io/otel/sdk`, `github.com/aws/aws-sdk-go-v2/service/s3`, `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream`, `google.golang.org/grpc`, `picomatch`, `brace-expansion`

### Removed
- Dead stub files in `internal/engine/` (`pipeline.go`, `ttl.go`, `shutdown.go`) that were unused scaffolding; their responsibilities live in the root `laredo` package engine

## [0.1.0] - 2026-03-22

### Added
- **Kinesis source**: DynamoDB checkpoint for ACK/resume, S3 manifest schema discovery, shard split/merge handling
- **Fan-out client**: `LocalSnapshotPath` for fast restart, `WithIndex`/`LookupByIndex` for secondary indexes
- **OAM service**: `WatchStatus` server-streaming RPC for real-time event monitoring
- **Query service**: `Subscribe` server-streaming RPC with optional replay of existing rows
- **Docker**: `--init-dir` flag for `/docker-entrypoint-init.d/` config merging pattern
- **GoReleaser**: SHA-256 checksums, SPDX SBOM generation
- **CI**: Code coverage reporting in step summary
- **Tests**: Fan-out client unit tests, PG reconnect integration test, fan-out multi-client E2E test, fan-out benchmarks, test data generators
- **Docs**: Fan-out client library guide, custom source/target implementation guides, environment variables reference, error codes reference, four operational runbooks (slot invalidation, reset source, snapshot restore, scaling fan-out), gRPC API reference for Replication service
- `CONTRIBUTING.md` with development workflow and code style guide

[Unreleased]: https://github.com/zourzouvillys/laredo/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/zourzouvillys/laredo/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/zourzouvillys/laredo/releases/tag/v0.1.0
