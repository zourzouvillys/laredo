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
- **Embedded gRPC server.** The fan-out target serves the replication protocol on a gRPC listener (default port 4002), separate from the main OAM/Query port (4001). _Revised by [ADR-007](#adr-007-server-side-fan-out-wiring): the protocol is served by a single **engine-global** replication service that routes by table, not by a per-target listener — so multiple fan-out targets on one server share one 4002 listener rather than each binding their own port._
- **Single-process bottleneck.** All fan-out clients connect to the one Laredo instance that holds the fan-out target. If that instance fails, all downstream clients lose their connection. Clients handle this through reconnection with delta sync (resuming from their last-known position) or snapshot restore.
- **Journal and snapshot management.** The fan-out target must manage its own change journal (for delta sync) and snapshot lifecycle (for new clients or clients that fall too far behind). This adds complexity to the target implementation, but keeps it isolated from the engine core.

## ADR-006: Subscription-scoped server-side filtering on the fan-out

**Status:** Accepted

### Context

A single fan-out target multiplexes one table to many clients. When that table is
shared across logical partitions — the canonical case being a multi-tenant table
keyed by `tenant_id` — each client usually wants only its own slice. Three ways
to deliver that were considered:

1. **One pipeline (and fan-out target) per partition.** A separate pipeline,
   in-memory state map, journal, and snapshot lifecycle for every tenant. This
   does not scale to thousands of partitions and fragments the source's single
   replication slot's benefit.
2. **Client-side filtering.** Send every row to every client and let each discard
   what it does not want. Simple, but every partition's rows cross the wire to
   every client — wasteful, and for multi-tenant data an isolation violation
   (other tenants' rows leave the server).
3. **Server-side per-subscription filtering.** The client attaches column
   predicates to its `Sync` request; the server evaluates them and sends only
   matching rows and changes.

### Decision

Add an optional `filters` field (`repeated FieldPredicate`) to `SyncRequest`. The
replication service compiles the predicates once per stream and applies them
uniformly across the snapshot, journal catch-up, and live phases. Predicates
support `equals`, `prefix`, and `in`, are AND-combined, and evaluate against the
post-change row for inserts/updates and the pre-change row for deletes;
`TRUNCATE` always passes. Filtering lives entirely in `service/replication` and
the wire contract — the engine, the fan-out target's in-memory state, and the
journal are untouched.

### Consequences

- **Hard partition isolation.** Rows a client filters out never cross the wire,
  so one fan-out target can safely serve a multi-tenant table — each subscriber
  with a `tenant_id` equality predicate sees only its own rows.
- **No new pipeline per partition.** A single source slot, target, journal, and
  snapshot set serve every partition; partitioning is a per-subscription concern,
  not a per-pipeline one.
- **Uniform across phases.** Applying the same predicate to the snapshot, the
  catch-up delta, and the live stream is what keeps a filtered subscriber's view
  consistent — a row never appears in the snapshot only to have its updates
  silently dropped.
- **Filter columns are assumed immutable.** A row that changes its partition
  value is not handled specially: the old-value subscriber is not told it left.
  This matches real partition keys (`tenant_id`), and is documented rather than
  engineered around.
- **Filtered resume re-scans.** A filtered client resumes from its last *received*
  sequence; if a partition is silent long enough for the journal to prune past
  it, the client re-snapshots (filtered). This is correct, and bounded by the
  same journal-retention sizing that already governs slow consumers.
- **Whole table still resident.** Filtering reduces delivery, not memory — the
  fan-out target keeps the full table in memory. Memory-bounded partitioning, if
  ever needed, would be a separate concern.

## ADR-007: Server-side fan-out wiring

**Status:** Accepted

**Amends:** [ADR-005](#adr-005-fan-out-replication-as-a-target-type)

### Context

ADR-005 described each fan-out target as running "its own gRPC listener … each
fan-out target instance can bind to a different port." The replication service
that was actually built (`service/replication`) is **engine-global**:
`replication.New(engine)` answers every `Sync`, `GetReplicationStatus`,
`ListSnapshots`, and `FetchSnapshot` request by looking up the right
`*fanout.Target` in `engine.Targets()` using the table identifier carried in the
request. One service instance already serves every fan-out table — so the
"one listener per target" model never matched the code.

Wiring the stock `laredo-server` binary to serve a fan-out from HOCON forced the
question into the open: how many listeners, on which port, configured how. The
documented config block also referenced a persistent `snapshot { store; serializer }`
that no `target/fanout` option backs.

### Decision

1. **One engine-global replication service per server.** Built from the engine,
   it serves every `replication-fanout` target and routes by table. There is no
   per-target listener.
2. **One dedicated listener, default port 4002**, separate from the OAM/Query
   port (4001). This preserves ADR-005's separate-port intent — and every
   published client example that dials `:4002` — while dropping per-target ports.
3. **Mounted like OAM and Query.** The service is a Connect handler registered
   via `service.EnableReplication`, so it inherits the same TLS, graceful
   shutdown, and gRPC/gRPC-Web/Connect multiplexing (consistent with
   [ADR-002](#adr-002-connect-rpc-for-service-communication)).
4. **HOCON shape.** A top-level `fanout { grpc { port = 4002 } }` block
   configures the (engine-global) listener; each `type = replication-fanout`
   target configures its own `journal`, `snapshot` retention, `client_buffer`,
   `max_clients`, and `heartbeat_interval`, which map onto `target/fanout.New`
   options. `laredo-server` starts the listener **iff** at least one
   `replication-fanout` target is configured, defaulting the port when the block
   is omitted.
5. **Persistent snapshot store is out of scope here.** `target/fanout` snapshots
   are in-memory (interval + retention only); the durable
   `snapshot { store; store_config; serializer }` shape is removed from the
   config contract until an option backs it. Cold-tier replay
   ([EDR-0002](/edr)) is wired separately on the replication service via a
   read-only archive reader, not on the target.

### Consequences

- **`max_clients` is per target; the port is global.** The old per-target
  `grpc { port; max_clients }` block splits: `port` becomes the top-level
  `fanout.grpc.port`; `max_clients` stays on each target (it bounds that
  target's client registry).
- **No client-facing churn.** Clients still dial `:4002` and name their table;
  [ADR-006](#adr-006-subscription-scoped-server-side-filtering-on-the-fan-out)
  subscription filters are unchanged.
- **Single-process bottleneck (ADR-005) is unchanged.** The listener and its
  targets share one process; failover remains client reconnect by source
  position, plus cold-tier replay where an archive is registered.
- **Stream isolation is preserved.** Long-lived fan-out streams sit on their own
  port, away from short OAM/Query RPCs. Co-mounting on the OAM/Query port, or
  running several replication listeners, can be added later without a contract
  change.

See [EDR-0005](/edr/0005-archive-from-hocon) for how a fan-out target's
cold-tier archive is wired from the same HOCON config.

## Further reading

- [Architecture](/concepts/architecture) -- the three-layer design these decisions support
- [Design Specification](/internals/design-spec) -- the full spec that these ADRs informed
- [Fan-Out Guide](/guides/fan-out) -- operational guide for the fan-out target
- [Configuration Reference](/reference/configuration) -- HOCON configuration details
