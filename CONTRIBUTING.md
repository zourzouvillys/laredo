# Contributing to Laredo

Thank you for your interest in contributing to laredo. This guide covers how to set up your development environment, run tests, and submit changes.

## Prerequisites

- Go 1.26+
- Docker (for integration tests with testcontainers)
- `golangci-lint` v2 (optional, CI enforces it)

```bash
# macOS
brew install go golangci-lint

# Verify
go version
golangci-lint version
```

## Getting started

```bash
git clone https://github.com/zourzouvillys/laredo.git
cd laredo
go build ./...
go test ./...
```

## Development workflow

1. Create a branch for your change.
2. Write code and tests.
3. Run the test and lint suite locally.
4. Commit with a descriptive message.
5. Open a pull request.

## Running tests

```bash
# Unit tests (fast, no external dependencies)
go test ./...

# Unit tests with race detector
go test -race ./...

# Integration tests (requires Docker for PostgreSQL testcontainer)
go test -tags=integration ./test/integration/...

# End-to-end tests
go test -tags=e2e ./test/e2e/...

# Benchmarks
go test -bench=. ./test/bench/...
```

## Linting

```bash
golangci-lint run ./...
```

The linter configuration is in `.golangci.yml`. All code must pass lint before merging.

## Code style

- Format with `gofumpt` (enforced by golangci-lint).
- Exported types need doc comments; internal code: comment only when non-obvious.
- Use `fmt.Errorf` with `%w` for error wrapping.
- Pass `context.Context` as the first parameter to functions that do I/O.
- No global state. All configuration via constructor options.
- No mocks in tests. Use real instances and `source/testsource`.
- Table-driven tests for pure functions.

## Project structure

See the package table in `CLAUDE.md` for a complete listing. Key directories:

| Directory | Purpose |
|---|---|
| `laredo` (root) | Public API: interfaces, types, engine builder |
| `internal/engine/` | Private engine implementation |
| `source/` | Source implementations (pg, kinesis, testsource) |
| `target/` | Target implementations (memory, httpsync, fanout) |
| `service/` | gRPC service implementations (oam, query, replication) |
| `client/fanout/` | Fan-out replication client library |
| `cmd/laredo-server/` | Server binary |
| `cmd/laredo/` | CLI tool |
| `test/` | Integration tests, e2e tests, benchmarks, test utilities |
| `website/` | Documentation site (Docusaurus) |

## Testing requirements

- Every change must include tests.
- Coverage must remain above 80%.
- No mocks. Use `source/testsource` for engine tests.
- Integration tests use testcontainers for real PostgreSQL.

## Documentation

When making changes that affect user-facing behavior, update the corresponding documentation in `website/docs/`. See `website/CLAUDE.md` for the doc structure.

## Protobuf

If you modify `.proto` files in `proto/`, regenerate the Go code:

```bash
make proto
```

This requires `buf` to be installed.

## Commit messages

Write concise commit messages that explain the "why" rather than the "what". Use present tense ("Add feature" not "Added feature").

## Pull requests

- Keep PRs focused on a single change.
- Include tests.
- Update documentation if applicable.
- Ensure all CI checks pass.
