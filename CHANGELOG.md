# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
