# Laredo

Real-time data sync library and service. Captures consistent baseline snapshots from data sources (PostgreSQL logical replication, S3+Kinesis), then streams all subsequent changes through pluggable targets (indexed in-memory, compiled in-memory, HTTP sync, replication fan-out). The engine manages pipelines, ACK coordination, backpressure, error isolation, TTL, and snapshots.

## Prerequisites

Requires Go 1.23+.

```bash
# macOS
brew install go

# Linux (Debian/Ubuntu)
sudo apt install -y golang-go

# Other / manual
# Download from https://go.dev/dl/
```

## Build & Test

```bash
go build ./...                          # build all packages
go test ./... -count=1                  # run unit tests
go test -race ./...                     # run tests with race detector
go test -tags=integration ./test/integration/...  # integration tests (requires PostgreSQL)
golangci-lint run                       # lint (must pass before pushing)
make proto                              # regenerate protobuf (requires buf)
```

Shorthand via Makefile:

```bash
make build            # go build ./...
make test             # go test ./...
make lint             # golangci-lint run ./...
make test-integration # integration tests
make proto            # buf generate
make docker           # docker build
make release-snapshot # local goreleaser dry-run
```

## Release

Tags trigger GoReleaser via GitHub Actions, which builds binaries, Docker images, and release archives.

```bash
git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0
```

**Release artifacts**:
- `laredo-server`: Linux + macOS (amd64/arm64)
- `laredo`: CLI client, Linux + macOS (amd64/arm64)
- Docker: `ghcr.io/zourzouvillys/laredo-server`

## Architecture

Three-layer design:

```
┌─────────────────────────────────────────────────────────┐
│  Pre-built Service ("laredo-server")                    │
│  HOCON config, gRPC (OAM + Query), metrics, logging,   │
│  signal handling, health endpoints, CLI tool             │
├─────────────────────────────────────────────────────────┤
│  Optional Modules                                       │
│  gRPC services, config loader, metrics bridges           │
├─────────────────────────────────────────────────────────┤
│  Core Library                                           │
│  Engine, SyncSource, SyncTarget, SnapshotStore,          │
│  pipeline filters/transforms, TTL, dead letter,          │
│  EngineObserver, readiness signaling                     │
│  Zero opinions on config, transport, or logging          │
└─────────────────────────────────────────────────────────┘
```

Pipeline model: each pipeline binds (source, table, filters, transforms, target). Sources are shared across pipelines. The engine demuxes changes by table and dispatches to targets. ACK advances only after all targets on a source confirm durability.

## Package Structure

| Package | Owns |
|---|---|
| `laredo` (root) | Public API: all interfaces (`SyncSource`, `SyncTarget`, `Engine`, `EngineObserver`, `SnapshotStore`, `SnapshotSerializer`), all types (`TableIdentifier`, `Row`, `ColumnDefinition`, `Value`, `Position`, `ChangeEvent`, `ChangeAction`, `PipelineState`, `SourceState`, `OrderingGuarantee`, `BufferPolicy`, `ErrorPolicyKind`, `ValidationError`), builder options (`WithSource`, `WithPipeline`, `WithObserver`, etc.), `NewEngine()` |
| `internal/engine/` | Private engine implementation: pipeline orchestrator, change buffer, ACK tracker, TTL manager, readiness tracker, graceful shutdown coordinator |
| `source/pg/` | PostgreSQL logical replication source (ephemeral + stateful modes, publication management, reconnection) |
| `source/kinesis/` | S3 baseline + Kinesis change stream source |
| `source/testsource/` | In-memory test source for integration testing |
| `target/httpsync/` | HTTP sync target (batched POST, retry, durability tracking) |
| `target/memory/` | `IndexedTarget` (raw rows + secondary indexes) and `CompiledTarget` (domain objects via compiler function) |
| `target/fanout/` | Replication fan-out target (in-memory state, change journal, snapshots, embedded gRPC server) |
| `snapshot/local/` | Local disk snapshot store |
| `snapshot/s3/` | S3 snapshot store |
| `snapshot/jsonl/` | JSONL snapshot serializer |
| `filter/` | Built-in pipeline filters: `FieldEquals`, `FieldPrefix`, `FieldRegex` |
| `transform/` | Built-in pipeline transforms: `DropFields`, `RenameFields`, `AddTimestamp` |
| `deadletter/` | Dead letter store interface + implementations |
| `service/` | gRPC server setup |
| `service/oam/` | OAM gRPC service (status, admin, snapshot management, dead letters, replay) |
| `service/query/` | Query gRPC service (lookup, list, count, subscribe) |
| `service/replication/` | Fan-out replication gRPC service |
| `config/` | HOCON config loading and validation |
| `metrics/prometheus/` | Prometheus `EngineObserver` implementation |
| `metrics/otel/` | OpenTelemetry `EngineObserver` implementation |
| `client/fanout/` | Go client for the replication fan-out protocol |
| `cmd/laredo-server/` | Pre-built service binary: config loading, engine setup, gRPC, health HTTP, metrics, signal handling |
| `cmd/laredo/` | CLI tool: subcommands for status, query, watch, reload, snapshot, etc. |
| `test/testutil/` | Test helpers: `TestObserver`, sample data factories, PostgreSQL testcontainer setup |
| `test/integration/` | Integration tests (build tag `integration`, require external services) |
| `test/e2e/` | End-to-end tests |
| `test/bench/` | Benchmarks |
| `proto/` | Protobuf definitions |
| `gen/` | Generated protobuf Go code (committed) |
| `website/` | Docusaurus docs site. See [`website/CLAUDE.md`](website/CLAUDE.md) for structure and sync rules. |

## Testing

### Philosophy

- **No mocks.** Use real instances for unit tests. The `source/testsource` package exists for this purpose — it provides a programmable in-memory source.
- **Table-driven tests** for all pure functions and type behavior.
- **`testutil` helpers** for common setup (sample tables, columns, rows, observer).
- **Integration tests** use testcontainers for real PostgreSQL. Tagged with `integration` build tag.
- **Always verify after changes**: run `go test ./...` after any code change. Tests must pass.

### Test tiers

| Tier | Location | What | How to run |
|---|---|---|---|
| Unit | `*_test.go` next to code | Package-level, fast, no I/O | `go test ./...` |
| Integration | `test/integration/` | Real PostgreSQL, real gRPC | `go test -tags=integration ./test/integration/...` |
| E2E | `test/e2e/` | Full server from config | `go test -tags=e2e ./test/e2e/...` |
| Bench | `test/bench/` | Performance | `go test -bench=. ./test/bench/...` |

### Writing tests

- Put `_test.go` files next to the code they test.
- Use `testutil.SampleTable()`, `testutil.SampleColumns()`, `testutil.SampleRow()` for consistent test data.
- Use `testutil.TestObserver` to capture and assert on engine events.
- For target tests: create target, call `OnInit`, feed rows via `OnBaselineRow`/`OnBaselineComplete`, then exercise the query API.
- For source tests: use `source/testsource` — it lets you programmatically emit baseline rows and change events.

## TODO.md

**`TODO.md` must be kept up to date.** It is the single source of truth for remaining work toward v1.0.

- When you discover work that needs to be done in the future, add it to `TODO.md` in the appropriate section.
- When you skip or defer work during implementation, add it to `TODO.md` with context on why it was deferred.
- When you complete a TODO item, check it off (`- [x]`).
- When a TODO item turns out to be unnecessary, remove it with a brief note in the commit message.

## Documentation

The documentation site lives in `website/` (Docusaurus). See [`website/CLAUDE.md`](website/CLAUDE.md) for the full doc structure and build instructions.

**When making code changes, update the docs.** If a change affects CLI commands, gRPC API, config options, Go library API, health endpoints, metrics, or any user-facing behavior, update the corresponding page in `website/docs/` and the root `README.md`. Stale docs are worse than no docs.

## Conventions

- Run `golangci-lint run` before pushing (CI enforces it, config in `.golangci.yml`)
- Go 1.23+, modern patterns (standard library where possible)
- No mocks in tests — use real instances and `source/testsource`
- `gofumpt` for formatting (enforced by golangci-lint)
- Exported types need doc comments; internal code: comment only when non-obvious
- Errors: use `fmt.Errorf` with `%w` for wrapping. Don't create sentinel errors unless they're part of the public API
- Context: pass `context.Context` as the first parameter to functions that do I/O or may block
- No global state. All configuration via constructor options
- Sequence numbers start at 1 (0 = no position)
- Project name: "laredo" everywhere (not "tablesync" — that's the spec's working name)
- Binary names: `laredo-server` (service), `laredo` (CLI)
- Package naming avoids stdlib collisions: `target/httpsync/` not `target/http/`, `source/testsource/` not `source/test/`
- Protobuf regeneration: `make proto` (requires `buf`)

## Configuration

`laredo-server` supports HOCON config files. Resolution order (later overrides earlier):

1. Built-in defaults
2. Config file (`--config` flag or `LAREDO_CONFIG` env var)
3. Config directory (`/etc/laredo/conf.d/*.conf`, alphabetical)
4. Environment variables (`LAREDO_SOURCES_PG_MAIN_CONNECTION`)
5. CLI flags (`--set key=value`)

## Spec

Full design specification is in [`docs/spec.md`](docs/spec.md).

## Sub-directory CLAUDE.md files

Some packages have their own `CLAUDE.md` with package-specific conventions:

- [`internal/engine/CLAUDE.md`](internal/engine/CLAUDE.md) — engine implementation rules
- [`source/pg/CLAUDE.md`](source/pg/CLAUDE.md) — PostgreSQL source conventions
- [`target/memory/CLAUDE.md`](target/memory/CLAUDE.md) — in-memory target conventions
- [`target/fanout/CLAUDE.md`](target/fanout/CLAUDE.md) — fan-out target conventions
- [`test/testutil/CLAUDE.md`](test/testutil/CLAUDE.md) — test infrastructure rules
- [`website/CLAUDE.md`](website/CLAUDE.md) — documentation site structure and sync rules
