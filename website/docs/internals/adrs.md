---
sidebar_position: 2
title: Architecture Decision Records
---

# Architecture Decision Records

This page documents key design decisions for the Laredo project. Each ADR records the context, decision, and consequences for a significant architectural choice.

---

## ADR-001: Go interfaces for source/target abstraction

**Status:** Accepted

### Context

Laredo needs a pluggable system for data sources (PostgreSQL, Kinesis, etc.) and data targets (in-memory, HTTP, fan-out, etc.). The engine must work with any source or target without knowing its implementation. Three approaches were considered:

1. **Go interfaces** -- define `SyncSource` and `SyncTarget` as interface types. Implementations satisfy the interface by implementing the methods.
2. **Code generation** -- define sources and targets in a schema language (e.g., protobuf) and generate Go code for each implementation.
3. **Reflection-based dispatch** -- register handler functions at runtime and use reflection to route calls.

### Decision

Use Go interfaces. `SyncSource` and `SyncTarget` are defined as interfaces in the root `laredo` package. Each implementation (e.g., `source/pg`, `target/memory`) provides a concrete type that satisfies the interface.

### Consequences

- **Compile-time safety.** The compiler verifies that every implementation satisfies the full interface contract. Missing methods are caught at build time, not at runtime.
- **No build-time dependencies.** No code generators, schema files, or build plugins are needed. A `go build` is sufficient.
- **Straightforward testing.** Test sources and targets (e.g., `source/testsource`) are just another implementation of the same interfaces.
- **Interface evolution requires care.** Adding a method to `SyncSource` or `SyncTarget` is a breaking change for all implementations. This is mitigated by keeping the interfaces stable and using optional interfaces (e.g., `Resettable`) for capabilities that not all implementations support.
- **No cross-language support from interfaces alone.** The Go interfaces define the in-process contract. Cross-language support is provided separately through the gRPC protocol (fan-out replication service, OAM, Query).

---

## ADR-002: Connect-RPC for service communication

**Status:** Accepted

### Context

Laredo exposes three gRPC services: OAM (operations and management), Query (row lookup and subscription), and Replication (fan-out protocol). A transport framework was needed that supports:

- Standard gRPC clients (grpc-go, grpc-java, etc.)
- Browser clients without a proxy (gRPC-Web)
- Simple HTTP clients for debugging (curl-friendly)
- Protobuf-based schema definition

Three options were evaluated:

1. **Pure gRPC (grpc-go)** -- the standard gRPC library for Go.
2. **Connect-RPC (connectrpc.com/connect)** -- a protocol-compatible alternative that supports gRPC, gRPC-Web, and its own Connect protocol over standard HTTP.
3. **REST/JSON API** -- hand-written HTTP handlers with OpenAPI.

### Decision

Use Connect-RPC. All services are defined in protobuf and served via Connect, which handles gRPC, gRPC-Web, and Connect protocols on a single port.

### Consequences

- **Triple protocol support.** A single server port serves gRPC (binary framing), gRPC-Web (for browsers), and Connect (simple HTTP POST with JSON or protobuf). Clients choose the protocol that fits their environment.
- **Standard net/http integration.** Connect handlers are standard `http.Handler` values. They compose naturally with Go middleware (logging, auth, metrics) without gRPC interceptor abstractions.
- **No grpc-go dependency.** The `grpc-go` library has a large dependency tree and its own HTTP/2 server. Connect uses Go's standard `net/http` server, reducing binary size and complexity.
- **Protobuf remains the schema.** Service definitions are still `.proto` files, generated with `buf`. Existing gRPC clients work without modification.
- **Ecosystem gap.** Some gRPC tooling (e.g., grpcurl) works against Connect servers in gRPC mode, but not all tools support the Connect protocol natively. This has not been a practical issue since gRPC mode is always available.

---

## ADR-003: HOCON for configuration

**Status:** Accepted

### Context

The `laredo-server` binary needs a configuration format for defining sources, pipelines, targets, snapshot stores, and operational settings. Requirements:

- Hierarchical structure (sources contain tables, pipelines reference sources)
- Environment variable substitution for secrets (database passwords, AWS credentials)
- Config file merging (base config + environment-specific overrides)
- Comments for documentation
- Familiarity for operators managing JVM-based infrastructure

Formats considered:

1. **YAML** -- widely used, supports hierarchy, but no native variable substitution or file merging.
2. **TOML** -- simple, but deeply nested structures become verbose.
3. **HOCON** -- superset of JSON with variable substitution, file includes, and config merging.
4. **JSON** -- no comments, no variable substitution.

### Decision

Use HOCON (Human-Optimized Config Notation) via the `github.com/gurkankaymak/hocon` library.

### Consequences

- **Environment variable substitution is built in.** Secrets like `connection = ${POSTGRES_URL}` work without wrapper scripts or template engines.
- **Config merging.** The `conf.d/` directory pattern works naturally: HOCON merges files by key path, so environment-specific overrides only need to specify the keys they change.
- **File includes.** Large configurations can be split across files with `include` directives.
- **Comments.** Operators can document configuration inline using `//` or `#` comments.
- **Less common format.** Operators unfamiliar with HOCON (common in JVM ecosystems, less so elsewhere) face a learning curve. The syntax is close enough to JSON that the basics are intuitive, but features like substitution and merging need documentation.
- **Library maturity.** The Go HOCON library is less battle-tested than YAML or TOML parsers. Edge cases in the HOCON spec may not be fully supported. In practice, the subset used by Laredo configurations is well covered.

---

## ADR-004: No mocks in tests

**Status:** Accepted

### Context

Laredo's test strategy needed to balance test speed, reliability, and confidence. The core interfaces (`SyncSource`, `SyncTarget`, `EngineObserver`) have many methods, and mock-based testing would require maintaining mock implementations that mirror the real behavior.

Three approaches were considered:

1. **Mock frameworks** (e.g., gomock, mockery) -- auto-generate mock implementations from interfaces.
2. **Hand-written mocks** -- manually implement test doubles for each interface.
3. **Real implementations** -- use actual implementations for all tests. Provide a purpose-built test source for unit tests and testcontainers for integration tests.

### Decision

No mocks. All tests use real implementations.

- **Unit tests** use `source/testsource`, a programmable in-memory source that lets tests control exactly when baseline rows and change events are emitted.
- **Target tests** use real target instances (`memory.IndexedTarget`, etc.) with data fed through the standard `OnInit`/`OnBaselineRow`/`OnBaselineComplete` lifecycle.
- **Observer tests** use `testutil.TestObserver`, which captures events into slices for assertion.
- **Integration tests** use testcontainers to spin up real PostgreSQL instances.

### Consequences

- **Higher confidence.** Tests exercise real code paths, including serialization, state management, and error handling. Bugs caused by mock/real behavior divergence are eliminated.
- **Simpler test code.** No mock setup boilerplate, no expectation configuration, no verification steps. Tests create real objects and call real methods.
- **`testsource` is purpose-built.** It is a real `SyncSource` implementation, not a mock. It maintains proper state transitions, supports baseline and streaming, and validates method call ordering. Tests that misuse the source API fail the same way a real source would.
- **Slower integration tests.** Tests that require PostgreSQL take seconds to start a container. This is acceptable for the integration test tier and does not affect unit test speed.
- **Interface changes require updating `testsource`.** When `SyncSource` gains a new method, `testsource` must implement it. This is deliberate: it forces the test infrastructure to stay current with the real interface.

---

## ADR-005: Fan-out replication as a target type

**Status:** Accepted

### Context

Laredo needs to support multiplexing one PostgreSQL logical replication slot to N downstream consumers (fan-out). This avoids the need for N replication slots on the source database. The question was where to implement the fan-out logic:

1. **Separate service layer** -- a standalone proxy service that sits between PostgreSQL and downstream consumers, independent of the Laredo engine.
2. **Engine-level feature** -- built into the engine core, with special-case routing logic for fan-out pipelines.
3. **Target implementation** -- implement fan-out as a `SyncTarget` that receives rows and changes through the standard pipeline, and separately serves downstream clients via its own gRPC server.

### Decision

Implement fan-out as a target type (`target/fanout`). It satisfies the `SyncTarget` interface and plugs into the engine like any other target. Internally, it maintains an in-memory state map, a change journal, periodic snapshots, and an embedded gRPC server for the replication protocol.

### Consequences

- **No engine changes.** The engine treats a fan-out target the same as an indexed memory target or an HTTP sync target. Pipeline configuration, ACK coordination, error isolation, and snapshot scheduling all work without modification.
- **Composable with pipeline features.** Filters and transforms apply before the fan-out target sees the data. This means different fan-out pipelines can serve different filtered views of the same source table.
- **Self-contained deployment.** A single `laredo-server` instance can run in-memory targets for local queries and fan-out targets for remote consumers. No separate proxy service to deploy and manage.
- **Embedded gRPC server.** The fan-out target runs its own gRPC listener (default port 4002) for the replication protocol. This is separate from the main OAM/Query port (4001). Each fan-out target instance can bind to a different port if multiple fan-out pipelines are configured.
- **Single-process bottleneck.** All fan-out clients connect to the one Laredo instance that holds the fan-out target. If that instance fails, all downstream clients lose their connection. Clients handle this through reconnection with delta sync (resuming from their last-known position) or snapshot restore.
- **Journal and snapshot management.** The fan-out target must manage its own change journal (for delta sync) and snapshot lifecycle (for new clients or clients that fall too far behind). This adds complexity to the target implementation, but keeps it isolated from the engine core.

## Further reading

- [Architecture](/concepts/architecture) -- the three-layer design these decisions support
- [Design Specification](/internals/design-spec) -- the full spec that these ADRs informed
- [Fan-Out Guide](/guides/fan-out) -- operational guide for the fan-out target
- [Configuration Reference](/reference/configuration) -- HOCON configuration details
