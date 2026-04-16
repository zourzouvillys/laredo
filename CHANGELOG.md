# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
