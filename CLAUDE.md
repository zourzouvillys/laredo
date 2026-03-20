# Laredo — Development Guide

## Build & Test

```bash
make build          # go build ./...
make test           # go test ./...
make lint           # golangci-lint run ./...
make test-integration  # go test -tags=integration ./test/integration/...
```

## Project Structure

- Single Go module: `github.com/zourzouvillys/laredo`
- Public API is the root package (`laredo`). All interfaces and types live there.
- `internal/engine/` is the private engine implementation.
- Source/target/snapshot packages implement the root interfaces.

## Conventions

- Go 1.23+, use standard library where possible.
- Run `gofumpt` for formatting (enforced by golangci-lint).
- Exported types need doc comments. Internal code: comment only when non-obvious.
- Errors: use `fmt.Errorf` with `%w` for wrapping. Don't create sentinel errors unless they're part of the public API.
- Context: pass `context.Context` as the first parameter to functions that do I/O or may block.
- Tests: `_test.go` files next to the code they test. Integration tests go in `test/integration/` with build tag `integration`.
- No global state. All configuration via constructor options.

## Naming

- Project: "laredo" everywhere (not "tablesync" — that's the spec's working name).
- Binary: `laredo-server` (service), `laredo` (CLI).
- Package naming avoids stdlib collisions: `target/httpsync/` not `target/http/`, `source/testsource/` not `source/test/`.

## Spec

Full design specification is in `docs/spec.md`.
