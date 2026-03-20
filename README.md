# Laredo

A Go library and service for capturing baseline snapshots from data sources and streaming real-time changes through pluggable targets.

## Architecture

Laredo is organized in three layers:

1. **Core Library** — engine, interfaces, types, pipeline orchestration. Zero opinions on config format, transport, or logging.
2. **Optional Modules** — gRPC OAM/Query service, HOCON config loader, metrics bridges, source/target/snapshot implementations.
3. **Pre-built Service (`laredo-server`)** — a ready-to-run binary with HOCON config, gRPC, metrics, structured logging, and signal handling. Ships with the `laredo` CLI tool.

### Sources

- **PostgreSQL** — logical replication (ephemeral or stateful mode)
- **S3 + Kinesis** — S3 baseline snapshots with Kinesis change streams

### Targets

- **HTTP Sync** — forward changes as batched HTTP requests
- **Compiled In-Memory** — domain objects via pluggable compiler functions
- **Indexed In-Memory** — raw rows with configurable secondary indexes
- **Replication Fan-Out** — multiplex one source to N gRPC clients (snapshot + journal + live stream)

### Pipeline

Each pipeline binds a source table to a target, with optional filters and transforms. The engine manages baseline loading, change streaming, ACK coordination, backpressure, error isolation, TTL, and snapshots.

## Building

```bash
# Build all packages
make build

# Run tests
make test

# Lint
make lint

# Build Docker image
make docker
```

## Project Layout

```
laredo.go, types.go, source.go, ...   Core interfaces (root package)
internal/engine/                       Engine implementation
source/{pg,kinesis,testsource}/        Source implementations
target/{httpsync,memory,fanout}/       Target implementations
snapshot/{local,s3,jsonl}/             Snapshot store/serializer implementations
filter/, transform/                    Built-in pipeline filters and transforms
service/                               gRPC services (OAM, Query, Replication)
config/                                HOCON config loading
metrics/{prometheus,otel}/             Metrics bridges
client/fanout/                         Go fan-out client library
cmd/laredo-server/                     Pre-built service binary
cmd/laredo/                            CLI tool
```

## Library Usage

```go
engine, errs := laredo.NewEngine(
    laredo.WithSource("pg_main", pgSource),
    laredo.WithPipeline("pg_main", laredo.Table("public", "config_document"), memTarget),
    laredo.WithObserver(myObserver),
)
engine.Start()
engine.AwaitReady(30 * time.Second)
defer engine.Stop()
```

See [docs/spec.md](docs/spec.md) for the full design specification.
