# Table Sync — Full Design Specification

## 1. Overview

Table Sync is a **library and optional standalone service** that captures a consistent baseline snapshot of configured tables from one or more data sources, then streams all subsequent changes (inserts, updates, deletes, truncates) in real time. Changes are routed through a **pluggable target interface**, allowing different consumers to handle records in different ways.

The system is organized in three layers:

1. **Core Library (`tablesync-core`)** — the engine, all interfaces, types, pipeline orchestration. Zero opinions on config format, transport, or logging.
2. **Optional Modules** — gRPC OAM/Query service, HOCON config loader, metrics bridges, individual source/target/snapshot implementations.
3. **Pre-built Service (`tablesync-server`)** — a ready-to-run binary that wires everything together with HOCON config, gRPC, metrics, structured logging, and signal handling. Ships with a **Go CLI tool (`tsync`)**.

Embedded users pull in the core library plus whichever source/target modules they need. They configure programmatically via a builder API. Optionally they can attach the gRPC module for management, but it is not required. The pre-built service is just a `main()` that assembles all modules with config-driven setup.

---

## 2. Architecture

### 2.1 Module / Dependency Structure

The core library and pre-built service are written in **Go**. Java support will be added later. The fan-out replication protocol has client libraries in Go, Java, and TypeScript.

```
Go Modules:
  tablesync-core              Engine, interfaces, types. No transport, no config format.
  tablesync-source-pg         PostgreSQL logical replication source.
  tablesync-source-kinesis    S3 + Kinesis source.
  tablesync-source-test       In-memory test source for integration testing.
  tablesync-target-http       HTTP sync target.
  tablesync-target-memory     Compiled in-memory and indexed in-memory targets.
  tablesync-target-fanout     Replication fan-out module (separate from core).
                              Exposes a SyncTarget implementation but runs its own
                              gRPC server, journal, and snapshot management.
  tablesync-snapshot-s3       S3 snapshot store.
  tablesync-snapshot-local    Local disk snapshot store.
  tablesync-snapshot-jsonl    Default JSONL snapshot serializer.
  tablesync-grpc              gRPC OAM + Query service. Depends on core.
  tablesync-config            HOCON config loading. Depends on core.
  tablesync-metrics           Metrics bridges (Prometheus, OpenTelemetry). Depends on core.
  tablesync-server            Pre-built service binary. Pulls everything together.
  tsync                       Go CLI tool. Speaks gRPC.

Fan-Out Client Libraries:
  tablesync-client-go         Go client for the replication fan-out protocol.
  tablesync-client-java       Java client for the replication fan-out protocol.
  tablesync-client-ts         TypeScript client for the replication fan-out protocol.

Future:
  tablesync-core-java         Java port of the core library.
  tablesync-source-kafka      SQL + Kafka source.
  tablesync-target-rocksdb    RocksDB target with native snapshots.
```

### 2.2 Layer Diagram

```
┌─────────────────────────────────────────────────────────┐
│  Pre-built Service ("tablesync-server")                 │
│                                                         │
│  - HOCON config loading                                 │
│  - gRPC server (OAM + Query)                            │
│  - Metrics export (Prometheus, OpenTelemetry)            │
│  - Structured logging                                   │
│  - Graceful shutdown, signal handling                    │
│  - CLI tool (tsync)                                     │
│                                                         │
│  Depends on ↓                                           │
├─────────────────────────────────────────────────────────┤
│  Optional Modules                                       │
│                                                         │
│  - gRPC OAM/Query service (wire into any app)           │
│  - Config loader (HOCON → engine config objects)         │
│  - Metrics bridge (engine observer → metrics backend)    │
│                                                         │
│  Depends on ↓                                           │
├─────────────────────────────────────────────────────────┤
│  Core Library ("tablesync-core")                        │
│                                                         │
│  - Engine (orchestrator, pipeline management)            │
│  - SyncSource interface + built-in implementations      │
│  - SyncTarget interface + built-in implementations      │
│  - SnapshotStore / SnapshotSerializer interfaces        │
│  - Pipeline filters and transforms                      │
│  - TTL / expiry engine                                  │
│  - Dead letter store interface                           │
│  - Position, ChangeEvent, schema types                   │
│  - Listener / subscription model                         │
│  - EngineObserver interface (for metrics, logging, hooks)│
│  - Readiness signaling                                   │
│                                                         │
│  Zero opinions on config format, transport, or logging   │
└─────────────────────────────────────────────────────────┘
```

### 2.3 Pipeline Model

The engine manages a set of **pipelines**. Each pipeline is a binding of (source, table, target). Multiple tables can share a source. A single table can fan out to multiple targets.

```
Pipeline {
    id: String                    // e.g. "pg_main:public.config_document->indexed-memory"
    source: SyncSource            // shared — multiple pipelines can reference the same source instance
    table: TableIdentifier
    filters: List<PipelineFilter>
    transforms: List<PipelineTransform>
    target: SyncTarget
    buffer: ChangeBuffer
    errorPolicy: ErrorPolicy
    ttlPolicy: TtlPolicy?
}
```

Sources are instantiated once and shared. If a PostgreSQL source feeds three tables into different targets, that is one source instance, one replication stream, and three target instances. The engine demuxes changes from the source by table and dispatches to the correct targets.

**ACK coordination across fan-out**: When a table fans out to multiple targets, the engine ACKs the source position only after **all** targets for that source have confirmed `is_durable()`. The ACK position is the minimum confirmed position across all pipelines sharing that source.

---

## 3. Source Interface

A source provides two capabilities: a point-in-time snapshot (baseline) and an ordered change stream that picks up from where the snapshot left off. It also supports ACK/position-tracking semantics.

```
interface SyncSource {

    /**
     * Called on startup. Establish connection, discover table schemas,
     * prepare for baseline + streaming.
     * Returns the set of available tables and their column definitions.
     */
    init(config: SourceConfig) -> Map<TableIdentifier, List<ColumnDefinition>>

    /**
     * Validate that the configured tables exist in the source, that
     * the connection user has the required permissions (SELECT,
     * replication, publication management if applicable), and that
     * the source is in a usable state.
     *
     * Called by the engine before starting any baseline or streaming.
     * Returns a list of validation errors. Empty list = all good.
     */
    validate_tables(tables: List<TableIdentifier>) -> List<ValidationError>

    /**
     * Begin a consistent baseline snapshot for the given tables.
     * Calls the provided callback for each row.
     * The snapshot must represent a consistent point-in-time,
     * and the source must be able to stream changes from that
     * point forward once the baseline completes.
     *
     * Returns a position token (LSN, stream sequence number,
     * S3 object version, etc.) marking the end of the snapshot.
     */
    baseline(tables: List<TableIdentifier>,
             row_callback: (TableIdentifier, Map<String, Value>) -> void) -> Position

    /**
     * Begin streaming changes from the given position.
     * Calls the appropriate callback for each change.
     * Blocking / long-running. On transient connection failures,
     * the source should reconnect internally according to its
     * reconnect policy before surfacing an error to the engine.
     */
    stream(from: Position, handler: ChangeHandler)

    /**
     * Acknowledge that all changes up to this position have been
     * durably processed. The source can release retained resources
     * (WAL segments, stream checkpoints, etc.).
     * No-op for sources that don't track position.
     */
    ack(position: Position)

    /**
     * Whether this source supports resuming from a previously
     * ACKed position.
     * If false, every startup requires a full baseline.
     */
    supports_resume() -> bool

    /**
     * If supports_resume() is true, returns the last ACKed position.
     * Returns null if no prior position exists (first run).
     */
    last_acked_position() -> Position?

    // --- Position operations ---

    /**
     * Compare two positions. Returns negative if a < b,
     * zero if equal, positive if a > b.
     * Used by the engine for ACK coordination (minimum confirmed
     * position across pipelines sharing this source).
     */
    compare_positions(a: Position, b: Position) -> int

    /**
     * Serialize a position to a string for storage in snapshot
     * metadata, gRPC responses, CLI display, etc.
     */
    position_to_string(p: Position) -> String

    /**
     * Deserialize a position from a string.
     * Returns error if the string is not a valid position for this source.
     */
    position_from_string(s: String) -> (Position, error)

    // --- Stream control ---

    /**
     * Pause the change stream without disconnecting.
     * The source retains its position/resources.
     */
    pause()

    /**
     * Resume after a pause.
     */
    resume()

    // --- Monitoring ---

    /**
     * Health / lag info for monitoring.
     */
    get_lag() -> LagInfo

    /**
     * What ordering guarantee this source provides.
     */
    ordering_guarantee() -> OrderingGuarantee

    /**
     * Current connection/streaming state of the source.
     */
    state() -> SourceState

    /**
     * Clean shutdown.
     */
    close()
}

enum SourceState {
    CONNECTING          // Initial connection or reconnecting after failure.
    CONNECTED           // Connected, not yet streaming (e.g., during baseline).
    STREAMING           // Actively streaming changes.
    RECONNECTING        // Lost connection, attempting to reconnect.
    PAUSED              // Paused by engine request.
    ERROR               // Unrecoverable error. Engine should stop pipelines on this source.
    CLOSED              // Shut down.
}

enum OrderingGuarantee {
    TOTAL_ORDER             // All changes across all tables in commit order (PostgreSQL).
    PER_TABLE_ORDER         // Ordered within a table, no cross-table guarantee.
    PER_PARTITION_ORDER     // Ordered within a partition/shard (Kinesis, Kafka).
    BEST_EFFORT             // No strong ordering.
}

ValidationError {
    table: TableIdentifier?     // null for source-level errors
    code: String                // e.g. "TABLE_NOT_FOUND", "PERMISSION_DENIED", "SLOT_INVALID"
    message: String
}
```

**The `Position` type** is opaque to the engine. Each source defines what it means. For PostgreSQL it is an LSN. For Kinesis it is a sequence number per shard. For S3+Kafka it might be a composite of an S3 version marker and Kafka offsets.

**Ephemeral vs. stateful** becomes a property of the source: `supports_resume()` tells the engine which startup path to take. A PostgreSQL source in ephemeral config returns false; in stateful config returns true. A Kinesis source with checkpointing returns true. An S3-snapshot-only source with no stream returns false.

---

## 4. Source Implementations

### 4.1 PostgreSQL Logical Replication

The primary source. Connects to PostgreSQL via logical replication protocol.

#### 4.1.1 Ephemeral Mode

On every startup:

1. Create a **temporary** logical replication slot (auto-dropped when the connection closes).
2. Perform a **full baseline load** of all configured tables using the exported snapshot.
3. Begin streaming changes from the slot.

If the process crashes or restarts, the temporary slot is gone. Next startup repeats the full cycle. There is no resume capability.

**WAL ACK behavior**: ACK immediately after the target's handler method returns. Since there is no durable slot to resume from, the ACK timing is irrelevant for recovery. It matters only to tell PostgreSQL it can reclaim WAL segments while the process is running.

**`supports_resume()`**: returns `false`.

**When to use**: Development, testing, or cases where the dataset is small enough that a full reload on every restart is acceptable.

#### 4.1.2 Stateful Mode

Uses a **named, persistent** replication slot that survives process restarts and database failovers.

**First startup (no existing slot)**:

1. Create a named persistent replication slot.
2. Perform a full baseline load using the exported snapshot.
3. Begin streaming changes.
4. **ACK the LSN only after the target's handler has confirmed the change is durably persisted** (e.g., HTTP response received, indexed store checkpointed). Premature ACK means PostgreSQL reclaims WAL that hasn't been fully processed, creating a gap on restart.

**Subsequent startup (slot already exists)**:

1. Connect to the existing named slot.
2. **Resume streaming from the last ACKed LSN.** PostgreSQL redelivers everything since the last ACK.
3. No baseline needed.

**Forced re-baseline (triggered via gRPC OAM)**:

1. Operator issues `ReloadTable` or `ReloadAll` via gRPC (or CLI).
2. Engine **pauses** the change stream (stops reading but keeps connection open so the slot retains WAL).
3. Engine calls `on_truncate` on all targets for the table to clear existing state.
4. Open a new snapshot connection. Start a `REPEATABLE READ` transaction using a current snapshot. Execute `SELECT *` for each configured table. Deliver rows to the targets via `on_baseline_row` / `on_baseline_complete`.
5. **Resume the change stream.** Apply any changes that accumulated during the reload. Some may overlap with rows just loaded — targets must be idempotent for this window.
6. ACK the current LSN position once reload and catch-up are complete.

**`supports_resume()`**: returns `true`.

**Slot management**:

- Slot name derived from service configuration (e.g., `tablesync_{service_id}`), stable across restarts.
- If the slot falls too far behind (`max_slot_wal_keep_size` exceeded), PostgreSQL invalidates it. The engine detects this, drops the invalidated slot, and performs a full re-baseline as if it were a first startup.
- Engine periodically logs slot lag (bytes behind WAL tip) as a health metric.

**WAL ACK behavior**: ACK is deferred until the target confirms persistence via `is_durable()`.

#### 4.1.3 Output Plugin

The PostgreSQL source uses the **`pgoutput`** logical decoding plugin, which is PostgreSQL's built-in protocol-level output plugin (available since PostgreSQL 10). It is the native output format for logical replication and requires no extensions to be installed. `pgoutput` works in conjunction with **publications** to determine which tables and which operations are replicated.

#### 4.1.4 Publication Management

The PostgreSQL source optionally manages a **publication** to control which tables and operations are delivered through the replication stream. This ensures the WAL stream only contains events the service cares about, reducing I/O and decoding overhead.

**Publication modes:**

1. **Use existing** (`publication.create = false`, the default) — the source connects to a pre-existing publication. The operator is responsible for creating and maintaining it. The source will fail on startup if the publication does not exist.

2. **Auto-create and manage** (`publication.create = true`) — the source creates the publication on first startup if it does not exist, and keeps it in sync with the configured tables. On subsequent startups, if the configured table set has changed, the source **alters** the publication to add or remove tables. This requires the connection user to have `CREATE` privilege on the database (or be a superuser / replication role).

**Publication scope:**

When `create = true`, the source builds the publication from the table list configured in the `tables` section of the config. Only tables bound to this source are included. The publication is scoped to specific operations via the `publish` setting.

**Startup flow with `create = true`:**

```
on startup:
    1. Check if publication exists (SELECT FROM pg_publication WHERE pubname = ...).
    2. If not exists:
        a. Collect all tables configured for this source.
        b. CREATE PUBLICATION {name} FOR TABLE {table1}, {table2}, ...
           WITH (publish = '{publish_operations}');
    3. If exists:
        a. Query current publication tables (pg_publication_tables).
        b. Compare against configured tables.
        c. If tables added:   ALTER PUBLICATION {name} ADD TABLE {new_tables};
        d. If tables removed: ALTER PUBLICATION {name} DROP TABLE {old_tables};
        e. If publish operations changed: ALTER PUBLICATION {name} SET (publish = '...');
    4. Create or connect to replication slot referencing this publication.
    5. Proceed with baseline / streaming.
```

**Row filters and column lists (PostgreSQL 15+):**

When running against PostgreSQL 15 or later, publications support row filters and column lists. These are configured **directly on the source publication**, not derived from pipeline filters. This gives operators explicit control over what data enters the replication stream at the WAL level.

```sql
-- Example: publication with row filter and column list
CREATE PUBLICATION tablesync_warden_01_pub
  FOR TABLE public.config_document WHERE (status = 'active'),
           public.rules (instance_id, key, data, updated_at)
  WITH (publish = 'insert, update, delete, truncate');
```

When `create = true`, the source reads the `tables` publication config block and generates the appropriate `CREATE PUBLICATION` / `ALTER PUBLICATION` statements with `WHERE` clauses and column lists as specified. The pipeline-level filter/transform chain still runs independently — publication-level filtering reduces WAL volume, while pipeline filters handle any additional per-target filtering.

**Publication naming:**

When `create = true`, the publication name defaults to `{slot_name}_pub` (e.g., `tablesync_warden_01_pub`). It can be overridden with `publication.name`.

**Cleanup:**

On `ResetSource`, if the publication was auto-created, the source drops and recreates both the slot and the publication. On service decommission, the operator can use `tsync reset-source --drop-publication` to clean up the publication as well.

#### 4.1.5 Connection Management and Reconnection

The PostgreSQL source maintains two connections:

1. **Replication connection** — the long-lived streaming replication connection.
2. **Query connection** — used for baseline `SELECT *`, `pg_catalog` queries, snapshot operations, and health checks.

**Reconnect strategy:**

On transient connection failure (network blip, PostgreSQL restart, failover), the source attempts to reconnect automatically before surfacing an error to the engine. The state machine:

```
CONNECTING ──► CONNECTED ──► STREAMING
     ▲              │              │
     │              │         (connection lost)
     │              │              │
     │              ▼              ▼
     │         RECONNECTING ◄─────┘
     │              │
     │         (max retries exceeded)
     │              │
     │              ▼
     └──────── ERROR
```

Reconnect behavior:

- On connection loss during streaming: transition to `RECONNECTING`. Attempt reconnect with exponential backoff.
- On successful reconnect (stateful mode): the slot still exists, resume streaming from the last ACKed LSN. No data loss.
- On successful reconnect (ephemeral mode): the temporary slot is gone. The source signals the engine that a full re-baseline is needed.
- After `max_reconnect_attempts` failures: transition to `ERROR`. The engine stops all pipelines on this source. Operator intervention required.
- During reconnect: the engine's pipelines for this source are paused. Other sources are unaffected.

```hocon
reconnect {
  max_attempts = 10
  initial_backoff = 1s
  max_backoff = 60s
  backoff_multiplier = 2.0
}
```

#### 4.1.6 Configuration

```hocon
sources {
  pg_main {
    type = postgresql
    connection = "postgresql://user:pass@host:5432/dbname"
    slot_mode = stateful              # or "ephemeral"
    slot_name = tablesync_warden_01

    publication {
      name = tablesync_warden_01_pub  # optional, defaults to {slot_name}_pub
      create = true                   # auto-create and manage publication
      publish = "insert, update, delete, truncate"

      # Per-table publication parameters (PostgreSQL 15+).
      # These control what data enters the WAL stream at the source level.
      # Separate from pipeline-level filters/transforms.
      tables {
        "public.config_document" {
          where = "status = 'active'"               # row filter
        }
        "public.rules" {
          columns = [instance_id, key, data, updated_at]  # column list
        }
        # Tables not listed here are published with all columns, no filter.
      }
    }

    reconnect {
      max_attempts = 10
      initial_backoff = 1s
      max_backoff = 60s
      backoff_multiplier = 2.0
    }
  }
}
```

Minimal configuration (use existing publication, no auto-management):

```hocon
sources {
  pg_main {
    type = postgresql
    connection = "postgresql://user:pass@host:5432/dbname"
    slot_mode = stateful
    slot_name = tablesync_warden_01
    publication {
      name = my_existing_publication
    }
  }
}
```

### 4.2 S3 + Kinesis

Baseline by reading an S3 object (or set of objects) representing a table snapshot. Stream from a Kinesis stream carrying CDC events.

**Baseline**: Read S3 objects. The snapshot format is defined by the S3 object contents (JSONL, Parquet, etc.). Schema can come from a manifest file in the S3 prefix or a schema registry.

**Streaming**: Consume Kinesis shards. Position is a composite of S3 object version + per-shard sequence numbers. ACK is Kinesis checkpoint.

**Ordering guarantee**: `PER_PARTITION_ORDER`.

**`supports_resume()`**: `true` if checkpointing is enabled.

**Demuxing**: If multiple tables' changes are multiplexed onto one stream, the source demuxes by table before dispatching to the engine. This is internal to the source.

#### 4.2.1 Configuration

```hocon
sources {
  kinesis_events {
    type = s3-kinesis
    baseline_bucket = "s3://my-bucket/snapshots/events/"
    baseline_format = jsonl
    stream_arn = "arn:aws:kinesis:us-east-1:123456:stream/cdc-events"
    consumer_group = tablesync-warden-01
    checkpoint_table = "dynamodb://tablesync-checkpoints"
    region = us-east-1
  }
}
```

### 4.3 SQL Database + Kafka

Baseline via `SELECT *` against any SQL-compatible source. Stream from a Kafka topic carrying CDC events from that database. Essentially variant 4.2 but with baseline coming from a live query rather than S3.

**Ordering guarantee**: `PER_PARTITION_ORDER`.

**`supports_resume()`**: `true` with Kafka consumer offset commits.

#### 4.3.1 Configuration

```hocon
sources {
  mysql_main {
    type = sql-kafka
    jdbc_url = "jdbc:mysql://host:3306/mydb"
    baseline_query_template = "SELECT * FROM {table}"
    kafka_brokers = "broker1:9092,broker2:9092"
    kafka_topic = "mydb.cdc"
    kafka_group_id = "tablesync-warden-01"
    schema_registry_url = "http://schema-registry:8081"
  }
}
```

---

## 5. Target Interface

```
interface SyncTarget {

    /**
     * Called once before baseline rows are delivered.
     * Receives the full column schema (column names, types,
     * nullability, primary key info) so the target can prepare
     * its internal structures.
     */
    on_init(table: TableIdentifier, columns: List<ColumnDefinition>)

    /**
     * Called for each row during the baseline snapshot phase.
     * Targets that need batching should buffer internally and
     * flush when their batch size is reached, then flush remainder
     * in on_baseline_complete.
     */
    on_baseline_row(table: TableIdentifier, row: Map<String, Value>)

    /**
     * Called once after all baseline rows have been delivered.
     * Finalization: flush batches, build indexes, mark ready.
     */
    on_baseline_complete(table: TableIdentifier)

    /**
     * Called when a new row is inserted (from change stream).
     */
    on_insert(table: TableIdentifier, columns: Map<String, Value>)

    /**
     * Called when an existing row is updated (from change stream).
     */
    on_update(table: TableIdentifier, columns: Map<String, Value>,
              identity: Map<String, Value>?)

    /**
     * Called when a row is deleted (from change stream).
     */
    on_delete(table: TableIdentifier, identity: Map<String, Value>)

    /**
     * Called when the table is truncated.
     */
    on_truncate(table: TableIdentifier)

    /**
     * Whether the last applied change has been durably persisted.
     * Controls source ACK in stateful mode.
     * In-memory targets return true unconditionally.
     * HTTP targets return true only after downstream confirmation.
     *
     * NOTE: In push mode with batching, the target may buffer changes
     * internally before flushing. The engine's ACKed position should
     * reflect only changes that have been durably handled, not just
     * accepted into a buffer. Targets that batch must only report
     * is_durable() == true for changes that have been flushed.
     * The engine tracks the position of the last change for which
     * is_durable() returned true.
     */
    is_durable() -> bool

    /**
     * Called when the source detects a column schema change.
     * The target should adapt its internal structures if possible.
     */
    on_schema_change(table: TableIdentifier,
                     old_columns: List<ColumnDefinition>,
                     new_columns: List<ColumnDefinition>) -> SchemaChangeResponse

    /**
     * Export all current state as a stream of entries.
     * Called by the engine during snapshot creation.
     * The engine pauses the stream before calling this.
     */
    export_snapshot() -> Iterator<SnapshotEntry>

    /**
     * Restore state from a snapshot. Called during startup before
     * streaming begins. Replaces on_init + baseline flow.
     * The target rebuilds its internal structures from the entries.
     */
    restore_snapshot(metadata: TableSnapshotInfo,
                     entries: Iterator<SnapshotEntry>)

    /**
     * Whether this target supports consistent snapshot export
     * without requiring the engine to pause the stream.
     * RocksDB: true. In-memory HashMap: false.
     *
     * NOTE: For now the engine always pauses regardless. This flag
     * is reserved for future optimization.
     */
    supports_consistent_snapshot() -> bool

    /**
     * Called on shutdown.
     */
    on_close(table: TableIdentifier)
}
```

### 5.1 Schema Change Handling

```
enum SchemaChangeAction {
    CONTINUE            // Target adapted, keep streaming.
    RE_BASELINE         // Target can't adapt incrementally, needs full reload.
    ERROR               // Target can't handle this change, stop pipeline.
}

SchemaChangeResponse {
    action: SchemaChangeAction
    message: String?
}
```

---

## 6. Target Implementations

### 6.1 HTTP Sync

**Purpose**: Forward every row change to a downstream HTTP service. The downstream owns the data; this target is stateless.

#### 6.1.1 Configuration

```hocon
targets {
  type = http-sync
  base_url = "https://downstream.example.com/api/sync"
  batch_size = 500
  flush_interval = 200ms
  timeout_ms = 5000
  retry_count = 3
  retry_backoff_ms = 500
  auth_header = "Bearer ..."
  headers {
    X-Source = table-sync
  }
}
```

#### 6.1.2 Behavior

**`on_init`**: Optionally validate connectivity. Notify downstream that a baseline is starting:

```
POST {base_url}/baseline/start
{ "table": {"schema": "public", "table": "config_document"}, "columns": [...] }
```

**`on_baseline_row`**: Buffer internally. When buffer reaches `batch_size`, flush:

```
POST {base_url}/baseline/batch
{
  "table": {"schema": "public", "table": "config_document"},
  "rows": [ {row1}, {row2}, ... ]
}
```

Retry with exponential backoff on failure. If all retries exhausted, propagate error (halts pipeline).

**`on_baseline_complete`**: Flush remaining buffer. Then:

```
POST {base_url}/baseline/complete
{ "table": {"schema": "public", "table": "config_document"} }
```

**`on_insert` / `on_update` / `on_delete` / `on_truncate`**: The HTTP target buffers changes internally and flushes as a batch when the buffer reaches `batch_size` or a `flush_interval` elapses (whichever comes first). Each flush sends a batch POST:

```
POST {base_url}/changes
{
  "table": {"schema": "public", "table": "config_document"},
  "changes": [
    {
      "action": "INSERT",
      "lsn": "0/1A2B3C4D",
      "xid": 12345,
      "timestamp": "2026-03-20T12:34:56Z",
      "columns": {...}
    },
    ...
  ]
}
```

**`is_durable`**: Returns `true` only after the batch has been flushed and the HTTP response received with 2xx status. While changes are buffered but not yet flushed, `is_durable()` returns `false` for those changes. The engine only advances the ACKed position up to the last change included in a successfully flushed batch — not to the latest change accepted into the buffer. This means if the process crashes with a partially filled buffer, the source will redeliver those buffered-but-unflushed changes on restart.

**`on_schema_change`**: Returns `CONTINUE` — forwards the new shape, lets downstream handle it.

**`on_close`**: Flush remaining buffer. Best-effort — if the flush fails on shutdown, those changes will be redelivered on restart (stateful mode) or part of the next baseline (ephemeral mode).

### 6.2 Compiled In-Memory

**Purpose**: Deserialize each row into a strongly-typed domain object via a pluggable compiler function. Maintain a live map of compiled objects keyed by configured fields.

#### 6.2.1 Configuration

```hocon
targets {
  type = compiled-memory
  compiler = ruleset
  key_fields = [instance_id, key]
  filter {
    field = key
    prefix = "rulesets/"
  }
}
```

#### 6.2.2 Concepts

A **compiler** is a registered function: `(row: Map<String, Value>) -> DomainObject`. Transforms raw row data into a typed representation. For example, a `"ruleset"` compiler parses the JSONB `data` column into a `RulesetDocument`.

The **key extractor** builds a composite lookup key from the configured `key_fields`.

The optional **filter** skips rows that don't match a predicate.

#### 6.2.3 Behavior

**`on_init`**: Allocate `ConcurrentHashMap`. Resolve compiler function by name. Validate key fields exist in column schema.

**`on_baseline_row`**: Apply filter. If passes, compile row, extract key, insert into map. Notify listeners with `(null, compiled_object)`.

**`on_baseline_complete`**: Log count. Mark state as READY.

**`on_insert`**: Same as `on_baseline_row`.

**`on_update`**: Apply filter. Compile new row. Extract key. Look up old compiled object. Replace in map. Notify listeners with `(old_compiled, new_compiled)`. If new values no longer pass the filter but old key exists, treat as delete.

**`on_delete`**: Extract key from identity. Remove from map. Notify listeners with `(old_compiled, null)`.

**`on_truncate`**: Clear the map. Notify listeners.

**`is_durable`**: Always `true` — memory is the store.

**`on_schema_change`**: Returns `RE_BASELINE` by default unless the user's compiler is known to be resilient.

**`on_close`**: Clear map, deregister listeners.

#### 6.2.4 Query API (exposed via gRPC)

- `get(key_values...) -> CompiledObject?`
- `all() -> Collection<CompiledObject>`
- `listen(listener) -> unsubscribe_fn` — replays existing entries on registration.

### 6.3 Indexed In-Memory Store

**Purpose**: General-purpose, schema-agnostic in-memory table replica with configurable secondary indexes for fast lookups. Stores raw rows without domain-specific transformation.

#### 6.3.1 Configuration

```hocon
targets {
  type = indexed-memory
  lookup_fields = [instance_id, key]
  additional_indexes = [
    { name = by_instance, fields = [instance_id], unique = false }
    { name = by_key_version, fields = [key, version], unique = true }
  ]
}
```

#### 6.3.2 Internal Data Structures

1. **Primary row store**: `HashMap<PrimaryKey, Row>` — keyed by `$id` column (or configured `primary_key`). Row is `Map<String, Value>`.
2. **Unique indexes**: `BiMap<CompositeKey, PrimaryKey>` — one-to-one mapping. Used for primary `lookup_fields` and `additional_indexes` with `unique: true`.
3. **Non-unique indexes**: `Map<CompositeKey, Set<PrimaryKey>>` — maps key tuple to multiple rows. Used for `additional_indexes` with `unique: false`.
4. **Column schema**: `List<ColumnDefinition>` fetched from source during init.

#### 6.3.3 Behavior

**`on_init`**: Validate all index fields exist in column schema. Allocate row store and all index structures.

**`on_baseline_row`**: Insert row into primary store. For each index, extract field values and insert mapping. Notify listeners with `(null, row)`.

**`on_baseline_complete`**: Log row count and index sizes. Mark READY.

**`on_insert`**: Same as `on_baseline_row`.

**`on_update`**: Look up old row by primary key. Replace in store. For each index: remove old entry, insert new entry. Notify with `(old_row, new_row)`.

**`on_delete`**: Remove from store. Remove from all indexes. Notify with `(old_row, null)`.

**`on_truncate`**: Clear store and all indexes. Notify.

**`is_durable`**: Always `true`.

**`on_schema_change`**: New column → `CONTINUE`. Dropped column that is part of an index → `RE_BASELINE` or `ERROR`. Type change on indexed field → `RE_BASELINE`.

**`on_close`**: Clear all structures.

#### 6.3.4 Query API (exposed via gRPC)

- `lookup(index_name?, key_values...) -> Row?` — single-row lookup on unique index.
- `lookup_all(index_name, key_values...) -> List<Row>` — multi-row lookup on non-unique index.
- `get(primary_key) -> Row?` — direct PK access.
- `all() -> Collection<Row>`
- `count() -> int`
- `listen(listener) -> unsubscribe_fn`

### 6.4 Replication Fan-Out

**Purpose**: Act as a replication multiplexer — absorb changes from a single upstream source (e.g., one PostgreSQL slot), maintain an in-memory state with periodic snapshots and a change journal, and serve any number of downstream client instances over gRPC. Clients connect, receive a consistent snapshot followed by a live change stream, and can maintain their own local state with fast in-memory lookups. This eliminates the need for each service instance to hold its own PostgreSQL replication slot.

This is conceptually similar to **Netflix Hollow** — a single producer maintains the authoritative dataset and publishes snapshots + deltas, and N consumers restore the latest snapshot and apply deltas to build a local in-memory replica.

#### 6.4.1 Architecture

```
                    PostgreSQL (1 slot)
                          │
                    ┌─────▼─────┐
                    │  Engine   │
                    │  (source) │
                    └─────┬─────┘
                          │
                ┌─────────▼──────────┐
                │  Replication       │
                │  Fan-Out Target    │
                │                    │
                │  ┌──────────────┐  │
                │  │ In-Memory    │  │
                │  │ State        │  │
                │  │ (current)    │  │
                │  └──────────────┘  │
                │  ┌──────────────┐  │
                │  │ Change       │  │
                │  │ Journal      │  │
                │  │ (bounded)    │  │
                │  └──────────────┘  │
                │  ┌──────────────┐  │
                │  │ Periodic     │  │
                │  │ Snapshots    │  │
                │  └──────────────┘  │
                │                    │
                │  gRPC Replication  │
                │  Service           │
                └─────┬──┬──┬───────┘
                      │  │  │
               ┌──────┘  │  └──────┐
               ▼         ▼         ▼
          Client A   Client B   Client C
          (ephemeral  (ephemeral  (has local
           instance)   instance)   snapshot,
                                   resumes)
```

#### 6.4.2 Internal Data Structures

1. **In-memory state**: `HashMap<PrimaryKey, Row>` — the current state of the table, identical to what the indexed-memory target maintains. This is the result of applying the baseline + all journal entries.

2. **Change journal**: A bounded, sequenced log of all changes applied since the last snapshot. Each entry has a monotonically increasing **sequence number** (local to this target, not the source position). The journal is the bridge between snapshots — any client that has a snapshot can catch up by replaying journal entries from where the snapshot left off.

   ```
   JournalEntry {
       sequence: int64              // monotonically increasing, target-local
       source_position: Position    // original source position (for reference)
       timestamp: Timestamp
       table: TableIdentifier
       action: ChangeAction         // INSERT, UPDATE, DELETE, TRUNCATE
       old_values: Map<String, Value>?
       new_values: Map<String, Value>?
   }
   ```

3. **Snapshots**: Periodic serialization of the full in-memory state, tagged with the journal sequence number at the time of capture. Stored via the pluggable `SnapshotStore` (same interface as the engine-level snapshots). Each snapshot is identified by its sequence number — a client holding snapshot at sequence S can resume by requesting journal entries from S+1 onward.

   ```
   FanOutSnapshot {
       snapshot_id: String
       sequence: int64              // journal sequence at time of snapshot
       source_position: Position
       table: TableIdentifier
       row_count: int64
       created_at: Timestamp
       columns: List<ColumnDefinition>
   }
   ```

4. **Client registry**: Tracks connected clients, their current sequence position, and backpressure state.

#### 6.4.3 Configuration

```hocon
targets {
  type = replication-fanout

  # Journal
  journal {
    max_entries = 1000000         # max journal entries retained
    max_age = 24h                 # max age of oldest journal entry
    # If a client is further behind than the journal covers,
    # it must re-fetch a full snapshot.
  }

  # Snapshots for client distribution
  snapshot {
    interval = 5m                 # how often to snapshot for client consumption
    store = local                 # local | s3
    store_config {
      path = "/var/lib/tablesync/fanout-snapshots"
    }
    serializer = jsonl
    retention {
      keep_count = 5
      max_age = 1h
    }
  }

  # gRPC server for client connections (separate from the OAM/Query gRPC)
  grpc {
    port = 4002
    max_clients = 500
    tls { enabled = false }
  }

  # Backpressure per client
  client_buffer {
    max_size = 50000              # max pending changes per client
    policy = drop_disconnect      # drop_disconnect | slow_down
    # drop_disconnect: if buffer fills, disconnect the client (it will reconnect and re-snapshot)
    # slow_down: apply backpressure to the client stream (risks backing up the journal)
  }
}
```

#### 6.4.4 Target Behavior

**`on_init`**: Allocate in-memory state, initialize empty journal at sequence 0, start the snapshot scheduler, start the gRPC replication server.

**`on_baseline_row`**: Insert row into in-memory state. Append to journal as an INSERT with the next sequence number.

**`on_baseline_complete`**: Mark READY. Take an initial snapshot (sequence 0 baseline). Begin accepting client connections.

**`on_insert`**: Insert into in-memory state. Append to journal. Broadcast to all connected clients.

**`on_update`**: Update in-memory state (keeping old values for the journal entry). Append to journal with both old and new values. Broadcast.

**`on_delete`**: Remove from in-memory state. Append to journal. Broadcast.

**`on_truncate`**: Clear in-memory state. Append a TRUNCATE entry to journal. Broadcast. Immediately take a new snapshot (clients that connect after a truncate need a clean baseline).

**`is_durable`**: `true` — the in-memory state is authoritative. Journal and snapshot persistence are best-effort for client distribution, not for source ACK purposes.

**`on_schema_change`**: Append a SCHEMA_CHANGE entry to the journal. Broadcast to clients. Take a new snapshot. Clients that can't handle the schema change will need to disconnect and re-snapshot.

**`on_close`**: Take a final snapshot. Disconnect all clients gracefully. Shut down gRPC server.

**`export_snapshot`**: Export current in-memory state (same as other in-memory targets).

**`restore_snapshot`**: Restore in-memory state, rebuild journal from the restore point.

#### 6.4.5 Journal Management

The journal is a bounded circular buffer. Entries older than `max_entries` or `max_age` are pruned. The **oldest retained sequence number** is tracked — this is the earliest point a client can resume from without needing a full snapshot.

```
journal_state:
    entries: CircularBuffer<JournalEntry>
    current_sequence: int64         // latest sequence number
    oldest_sequence: int64          // earliest sequence still in the journal
    snapshot_sequences: List<int64> // sequence numbers of available snapshots
```

When a snapshot is taken, the journal can optionally be compacted — entries older than the snapshot's sequence can be pruned more aggressively, since any client that needs to go further back will use the snapshot instead.

#### 6.4.6 Client Protocol (gRPC Replication Service)

The client connection protocol has three phases:

**Phase 1 — Handshake**: Client connects and declares what state it already has (if any).

**Phase 2 — Catch-up**: Based on the client's declared state, the server either:
- Sends a full snapshot + streams changes from the snapshot's sequence, OR
- Sends only the journal entries the client missed (delta catch-up).

**Phase 3 — Live streaming**: Once caught up, the client receives changes in real time.

The entire protocol runs over a single server-streaming gRPC call. The server sends a sequence of messages: first a handshake response, then snapshot chunks (if needed), then journal entries for catch-up, then live changes. The client sees a single ordered stream.

**Client resume logic:**

```
client connects with:
    last_known_sequence: int64?     // null if fresh client
    last_snapshot_id: String?       // snapshot the client already has locally

server decides:
    if last_known_sequence is null:
        // Fresh client. Send latest snapshot + stream from there.
        -> FULL_SNAPSHOT mode

    else if last_known_sequence >= journal.oldest_sequence:
        // Client is within journal range. Send delta only.
        -> DELTA mode (journal entries from last_known_sequence+1)

    else if last_snapshot_id is not null
            AND server still has that snapshot
            AND snapshot.sequence >= journal.oldest_sequence:
        // Client's snapshot is still valid but its journal position is too old.
        // Tell client to use its local snapshot, send delta from snapshot sequence.
        -> DELTA_FROM_SNAPSHOT mode (journal from snapshot.sequence+1)

    else:
        // Client is too far behind. Must re-snapshot.
        -> FULL_SNAPSHOT mode
```

**Atomic handoff from snapshot to stream**: When sending a full snapshot, the server pins the journal at the snapshot's sequence number. After all snapshot rows are sent, the server sends journal entries from sequence+1 through current, then transitions to live streaming. The client never misses a change and never sees a gap.

#### 6.4.7 Client-Side Snapshot Storage

Clients can optionally store snapshots locally (disk, S3, etc.) so that on restart they don't need to fetch a full snapshot from the server. The client protocol supports this:

1. Client starts up, checks local snapshot store.
2. If local snapshot exists: connect with `last_known_sequence = snapshot.sequence`. Server sends delta.
3. If no local snapshot: connect with `last_known_sequence = null`. Server sends full snapshot.
4. Client periodically saves new local snapshots at the current sequence for faster future restarts.

The server doesn't manage client-side snapshots — it only provides the data. Client-side storage is the client's responsibility, using whatever storage mechanism it prefers.

#### 6.4.8 Client Libraries

Client libraries are provided in **Go**, **Java**, and **TypeScript** for consuming the replication stream. All three follow the same pattern: connect to the fan-out gRPC service, receive a snapshot or delta, build local in-memory state, stream live changes, and auto-reconnect on failure.

**Go client:**

```go
client, err := fanoutclient.New(
    fanoutclient.ServerAddress("tablesync-server:4002"),
    fanoutclient.Table("public", "config_document"),

    // Optional: local snapshot cache for fast restart
    fanoutclient.LocalSnapshotPath("/var/lib/myapp/tablesync-cache"),

    // How to build the in-memory state on the client side
    fanoutclient.WithIndexedState(
        memory.LookupFields("instance_id", "key"),
        memory.AddIndex("by_instance", []string{"instance_id"}, false),
    ),

    fanoutclient.ClientID("myapp-instance-abc123"),
)

// Connect and sync — blocks until initial state is loaded
client.Start()
client.AwaitReady(30 * time.Second)

// Query the local replica
row, ok := client.Lookup("inst_abc", "rulesets/default")
rows := client.LookupAll("by_instance", "inst_abc")

// Subscribe to changes
client.Listen(func(old, new tablesync.Row) {
    // react to changes
})

// On shutdown — saves local snapshot for fast restart
client.Stop()
```

**Java client:**

```java
var client = FanOutClient.builder()
    .serverAddress("tablesync-server:4002")
    .table("public", "config_document")
    .localSnapshotPath("/var/lib/myapp/tablesync-cache")
    .withIndexedState(IndexConfig.builder()
        .lookupFields("instance_id", "key")
        .addIndex("by_instance", List.of("instance_id"), false)
        .build())
    .clientId("myapp-instance-abc123")
    .build();

client.start();
client.awaitReady(Duration.ofSeconds(30));

Row row = client.lookup("inst_abc", "rulesets/default");
client.listen((old, updated) -> { /* react */ });

client.stop();
```

**TypeScript client:**

```typescript
const client = new FanOutClient({
  serverAddress: "tablesync-server:4002",
  table: { schema: "public", table: "config_document" },
  localSnapshotPath: "/var/lib/myapp/tablesync-cache",
  indexedState: {
    lookupFields: ["instance_id", "key"],
    indexes: [{ name: "by_instance", fields: ["instance_id"], unique: false }],
  },
  clientId: "myapp-instance-abc123",
});

await client.start();
await client.awaitReady(30_000);

const row = client.lookup("inst_abc", "rulesets/default");
client.listen((old, updated) => { /* react */ });

await client.stop();
```

The client library internally:
1. Connects to the fan-out target's gRPC replication service.
2. Sends handshake with local snapshot sequence (if any).
3. Receives snapshot (or delta) and populates the local in-memory state.
4. Streams live changes and keeps the local state updated.
5. Periodically saves local snapshots.
6. Reconnects automatically on disconnect (with exponential backoff).

#### 6.4.9 gRPC Replication Service

```protobuf
syntax = "proto3";
package tablesync.replication.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/struct.proto";

service TableSyncReplication {

  // Primary replication stream. Client connects, declares its state,
  // receives catch-up data (snapshot or delta), then live changes.
  // Single long-lived server-streaming call.
  rpc Sync(SyncRequest) returns (stream SyncMessage);

  // List available snapshots that clients can use for bootstrapping.
  rpc ListSnapshots(ReplicationListSnapshotsRequest) returns (ReplicationListSnapshotsResponse);

  // Fetch a specific snapshot. Streaming response for large snapshots.
  rpc FetchSnapshot(FetchSnapshotRequest) returns (stream SnapshotChunk);

  // Get current server state (latest sequence, journal bounds, client count).
  rpc GetReplicationStatus(GetReplicationStatusRequest) returns (GetReplicationStatusResponse);
}

// ============================================================
// Sync (primary replication stream)
// ============================================================

message SyncRequest {
  string schema = 1;
  string table = 2;

  // Client's current state. Null/zero if fresh client.
  int64 last_known_sequence = 3;      // 0 = no prior state
  string last_snapshot_id = 4;        // empty = no local snapshot

  // Client identification (for monitoring, not auth).
  string client_id = 5;              // e.g. "myapp-instance-abc123"
}

// The server sends a stream of SyncMessage. The sequence is:
// 1. SyncHandshake (exactly once, first message)
// 2. If mode is FULL_SNAPSHOT:
//    a. SnapshotBegin
//    b. SnapshotRow (repeated, all rows)
//    c. SnapshotEnd
// 3. JournalEntry (repeated, catch-up entries from snapshot sequence or client sequence)
// 4. JournalEntry (ongoing, live changes as they happen)
//
// The transition from catch-up to live is seamless — the client
// just keeps consuming JournalEntry messages.

message SyncMessage {
  oneof message {
    SyncHandshake handshake = 1;
    SnapshotBegin snapshot_begin = 2;
    SnapshotRow snapshot_row = 3;
    SnapshotEnd snapshot_end = 4;
    ReplicationJournalEntry journal_entry = 5;
    SchemaChangeNotification schema_change = 6;
    Heartbeat heartbeat = 7;
  }
}

message SyncHandshake {
  SyncMode mode = 1;
  int64 server_current_sequence = 2;
  int64 journal_oldest_sequence = 3;
  int64 resume_from_sequence = 4;     // sequence the stream will start from
  repeated ColumnDefinition columns = 5;
  string snapshot_id = 6;            // if mode is FULL_SNAPSHOT, the snapshot being sent
}

enum SyncMode {
  SYNC_MODE_UNSPECIFIED = 0;
  SYNC_MODE_FULL_SNAPSHOT = 1;        // Server sends full snapshot + journal from snapshot seq
  SYNC_MODE_DELTA = 2;                // Server sends journal entries from client's last seq
  SYNC_MODE_DELTA_FROM_SNAPSHOT = 3;  // Client should use its local snapshot; server sends delta from snapshot seq
}

message SnapshotBegin {
  string snapshot_id = 1;
  int64 sequence = 2;                // journal sequence at time of snapshot
  int64 row_count = 3;               // expected total rows (for progress reporting)
}

message SnapshotRow {
  google.protobuf.Struct row = 1;
}

message SnapshotEnd {
  int64 sequence = 1;                // confirms the sequence this snapshot represents
  int64 rows_sent = 2;
}

message ReplicationJournalEntry {
  int64 sequence = 1;
  string source_position = 2;
  google.protobuf.Timestamp timestamp = 3;
  string action = 4;                 // INSERT, UPDATE, DELETE, TRUNCATE
  google.protobuf.Struct old_values = 5;
  google.protobuf.Struct new_values = 6;
}

message SchemaChangeNotification {
  int64 sequence = 1;
  repeated ColumnDefinition old_columns = 2;
  repeated ColumnDefinition new_columns = 3;
}

message Heartbeat {
  int64 current_sequence = 1;
  google.protobuf.Timestamp server_time = 2;
}

// Re-use ColumnDefinition from the OAM service.

// ============================================================
// Snapshot browsing
// ============================================================

message ReplicationListSnapshotsRequest {
  string schema = 1;
  string table = 2;
  int32 limit = 3;
}

message ReplicationListSnapshotsResponse {
  repeated ReplicationSnapshotInfo snapshots = 1;
}

message ReplicationSnapshotInfo {
  string snapshot_id = 1;
  int64 sequence = 2;
  string source_position = 3;
  int64 row_count = 4;
  int64 size_bytes = 5;
  google.protobuf.Timestamp created_at = 6;
}

message FetchSnapshotRequest {
  string snapshot_id = 1;
}

message SnapshotChunk {
  oneof chunk {
    SnapshotBegin begin = 1;
    SnapshotRow row = 2;
    SnapshotEnd end = 3;
  }
}

// ============================================================
// Status
// ============================================================

message GetReplicationStatusRequest {
  string schema = 1;
  string table = 2;
}

message GetReplicationStatusResponse {
  int64 current_sequence = 1;
  int64 journal_oldest_sequence = 2;
  int64 journal_entry_count = 3;
  int64 row_count = 4;
  int32 connected_clients = 5;
  repeated ConnectedClient clients = 6;
  ReplicationSnapshotInfo latest_snapshot = 7;
}

message ConnectedClient {
  string client_id = 1;
  int64 current_sequence = 2;       // where the client is in the stream
  int64 behind_count = 3;           // entries behind live
  int32 buffer_depth = 4;
  google.protobuf.Timestamp connected_at = 5;
  string state = 6;                 // "catching_up", "live", "backpressured"
}
```

#### 6.4.10 Heartbeats and Disconnect Detection

The server sends periodic heartbeat messages (default every 5s) on idle connections. This serves two purposes:

1. **Client liveness**: If the client's gRPC stream breaks, the server detects it and cleans up the client registration.
2. **Sequence tracking**: The heartbeat includes the current sequence number, so clients know they're up to date even during quiet periods.

Clients should treat a heartbeat gap (e.g., no message for 30s) as a connection failure and initiate reconnect.

#### 6.4.11 Consistency Guarantees

- **Snapshot + journal = complete state**: A snapshot at sequence S plus all journal entries from S+1 to current produces the exact same state as the server's in-memory state at the current sequence. No gaps, no duplicates.
- **Ordering**: Journal entries are delivered in strict sequence order. A client never sees entry N+1 before entry N.
- **Atomicity of snapshot-to-stream handoff**: The server pins the journal before sending the snapshot. After the snapshot is fully sent, the server sends all journal entries accumulated during the snapshot transfer, then transitions to live. The client sees a single ordered stream with no gap.
- **At-least-once for reconnecting clients**: If a client disconnects and reconnects with `last_known_sequence`, it may re-receive the entry at that sequence. Clients must be idempotent for the entry at their declared sequence.

### 6.5 Relationship Between Targets

```
                    ┌─────────────────────────┐
                    │      Engine              │
                    │  (Pipeline orchestrator) │
                    └────────────┬────────────┘
                                 │
                    SyncTarget interface
                                 │
        ┌────────────────┬───────┴───────┬────────────────┐
        │                │               │                │
  ┌─────▼──────┐  ┌──────▼────┐  ┌───────▼──────┐  ┌─────▼──────────┐
  │ HTTP Sync  │  │ Compiled  │  │ Indexed      │  │ Replication    │
  │            │  │ In-Memory │  │ In-Memory    │  │ Fan-Out        │
  │ Forwards   │  │           │  │              │  │                │
  │ changes as │  │ Domain    │  │ Raw rows +   │  │ Multiplexes    │
  │ HTTP reqs. │  │ objects   │  │ secondary    │  │ one source     │
  │            │  │ via       │  │ indexes.     │  │ slot to N      │
  │ Batches    │  │ compiler. │  │              │  │ gRPC clients.  │
  │ internally.│  │           │  │              │  │                │
  │            │  │           │  │              │  │ Snapshot +     │
  │ is_durable:│  │ is_durable│  │ is_durable:  │  │ journal +      │
  │ after HTTP │  │ always    │  │ always true. │  │ live stream.   │
  │ confirms.  │  │ true.     │  │              │  │                │
  │            │  │           │  │              │  │ is_durable:    │
  │            │  │           │  │              │  │ always true.   │
  └────────────┘  └───────────┘  └──────────────┘  └────────────────┘
```

Multiple targets can be attached to the same table (fan-out). The engine calls each target for each change; source ACK happens only after all targets return and all report `is_durable() == true`.

---

## 7. Pipeline Filters and Transforms

Filters and transforms sit between the source and target in each pipeline. The engine applies the chain before dispatching to the target.

```
interface PipelineFilter {
    /**
     * Return true to include this row, false to skip.
     * Applied to baseline rows and change events.
     */
    include(table: TableIdentifier, row: Map<String, Value>) -> bool
}

interface PipelineTransform {
    /**
     * Transform a row before it reaches the target.
     * Can modify, add, or remove fields.
     * Return null to drop the row entirely.
     */
    transform(table: TableIdentifier, row: Map<String, Value>) -> Map<String, Value>?
}
```

Multiple filters and transforms can be chained per pipeline, applied in order.

### 7.1 Built-in Filter / Transform Types

| Type | Kind | Description |
|---|---|---|
| `field-equals` | Filter | Match rows where a field equals a value. |
| `field-prefix` | Filter | Match rows where a string field starts with a prefix. |
| `field-regex` | Filter | Match rows where a field matches a regex. |
| `drop-fields` | Transform | Remove specified fields from the row. |
| `rename-fields` | Transform | Rename fields (map of old name → new name). |
| `add-timestamp` | Transform | Add a field with the current timestamp. |

Custom filters/transforms are provided via the library builder API.

---

## 8. Backpressure and Buffering

Each pipeline gets a bounded change buffer between the source dispatcher and the target.

### 8.1 Configuration

```hocon
buffer {
  max_size = 10000
  policy = block        # block | drop_oldest | error
}
```

### 8.2 Policies

- **block** — the source dispatcher blocks when the buffer is full. A slow target backs up the source, which slows all pipelines on that source. Safe default — no data loss.
- **drop_oldest** — drop the oldest undelivered change when full. Only safe for targets where the latest state matters and intermediate changes are disposable.
- **error** — mark the pipeline as ERROR when the buffer fills. Other pipelines continue.

---

## 9. Pipeline Error Isolation

Each pipeline has its own lifecycle state independent of the global service state.

### 9.1 Pipeline States

```
enum PipelineState {
    INITIALIZING
    BASELINING
    STREAMING
    PAUSED
    ERROR
    STOPPED
}
```

### 9.2 Error Policies

```hocon
error_handling {
  max_retries = 5
  retry_backoff_ms = 1000
  retry_backoff_max_ms = 30000
  on_persistent_failure = isolate   # isolate | stop_source | stop_all
  dead_letter {
    enabled = true
    store = s3
    config {
      bucket = my-bucket
      prefix = "dead-letter/"
    }
  }
}
```

- **isolate** — mark pipeline as ERROR, continue all others. Source ACK advances past this pipeline. Dead letter captures the failed change if enabled.
- **stop_source** — stop all pipelines on this source. Other sources continue.
- **stop_all** — halt the engine.

### 9.3 Dead Letter Store Interface

```
interface DeadLetterStore {
    write(pipeline_id: String, change: ChangeEvent, error: ErrorInfo)
    read(pipeline_id: String, limit: int) -> List<DeadLetterEntry>
    replay(pipeline_id: String, target: SyncTarget)
    purge(pipeline_id: String)
}
```

---

## 10. TTL / Expiry

In-memory targets can have automatic row expiry.

### 10.1 Modes

1. **Field-based** — a column in the row contains the expiry timestamp.
2. **Computed** — a user-provided function calculates the expiry from the row.

### 10.2 Configuration

```hocon
ttl {
  mode = field                  # or "computed"
  field = expires_at            # column name (field mode only)
  check_interval = 30s
}
```

### 10.3 Behavior

- On check interval, scan rows where expiry timestamp is in the past. Remove from target, update indexes. Emit `onRowExpired` through observer.
- On **update** where the new expiry is in the future: normal update, row stays alive. TTL scanner re-evaluates on next pass.
- On **update** where the new expiry is still in the past: treat as delete — remove from target immediately.
- On **insert** where the expiry is already in the past: skip insertion entirely.
- TTL check is per-pipeline, runs on the engine's scheduler, not the target's responsibility.

---

## 11. Snapshots

The engine can serialize the current state of any target at a point-in-time, tagged with the source position at that moment, and write it to a pluggable snapshot store. On startup, an instance can restore from a snapshot instead of doing a full baseline, then resume streaming from the snapshot's position.

### 11.1 Startup Paths

1. **Cold start** — no snapshot, no prior position. Full baseline from source.
2. **Resume** — no snapshot, but source supports resume from last ACKed position. Reconnect the stream.
3. **Snapshot restore** — load snapshot, resume streaming from the snapshot's position. Works even if the source doesn't natively support resume, as long as the stream has data going back to the snapshot point.

### 11.2 Startup Flow

```
on startup:
    1. Check snapshot store for latest snapshot matching this config.
    2. If snapshot exists:
        a. Load snapshot metadata, get source positions.
        b. Check if each source can stream from that position.
           - PostgreSQL stateful: can the slot still deliver from there?
           - Kinesis: is the sequence number still in retention?
        c. If yes:
           - Call target.restore_snapshot() for each pipeline.
           - Begin streaming from snapshot positions.
        d. If no (position too old, stream data expired):
           - Log warning, discard snapshot.
           - Fall through to full baseline.
    3. If no snapshot (or snapshot unusable):
        - Full baseline from source.
```

### 11.3 Snapshot Store Interface

```
interface SnapshotStore {

    /**
     * Write a snapshot.
     */
    save(snapshot_id: String, metadata: SnapshotMetadata,
         entries: Map<TableIdentifier, Iterator<SnapshotEntry>>) -> SnapshotDescriptor

    /**
     * Load a snapshot by ID.
     */
    load(snapshot_id: String) -> (SnapshotMetadata, Map<TableIdentifier, Iterator<SnapshotEntry>>)

    /**
     * Get snapshot metadata without loading data.
     */
    describe(snapshot_id: String) -> SnapshotDescriptor

    /**
     * List available snapshots, optionally filtered.
     */
    list(filter: SnapshotFilter?) -> List<SnapshotDescriptor>

    /**
     * Delete a snapshot.
     */
    delete(snapshot_id: String)

    /**
     * Delete all but the N most recent snapshots.
     */
    prune(keep: int, table: TableIdentifier?)
}
```

Implementations: `LocalDiskSnapshotStore`, `S3SnapshotStore`.

### 11.4 Snapshot Serializer Interface

```
interface SnapshotSerializer {

    /**
     * Format identifier. Used by stores for naming and discovery.
     */
    format_id() -> String           // "jsonl", "parquet", "protobuf", etc.

    /**
     * Serialize a stream of rows into an output stream.
     * Called per-table during snapshot creation.
     */
    write(table: TableSnapshotInfo, rows: Iterator<Row>, output: OutputStream)

    /**
     * Deserialize rows from an input stream.
     */
    read(input: InputStream) -> (TableSnapshotInfo, Iterator<Row>)
}
```

Default implementation: `JsonlSnapshotSerializer`.

### 11.5 Snapshot Metadata

```
SnapshotMetadata {
    snapshot_id: String
    created_at: Timestamp
    source_positions: Map<String, Position>     // per source
    tables: List<TableSnapshotInfo>
    format: String                              // "jsonl", etc.
    user_meta: Map<String, Value>               // opaque, user-provided
}

TableSnapshotInfo {
    table: TableIdentifier
    row_count: int64
    columns: List<ColumnDefinition>
    target_type: String
    indexes: List<IndexDefinition>
}
```

### 11.6 User Metadata

The engine accepts an opaque user metadata payload that is persisted alongside the snapshot and returned on restore. This is where users put config fingerprints, version info, or whatever they need to verify compatibility.

On restore, the user can inspect the metadata before committing:

```
descriptor = snapshotStore.describe(snapshotId)
if (!myApp.isCompatible(descriptor.metadata.user_meta)) {
    // reject, fall back to full baseline
}
engine.restoreSnapshot(snapshotId)
```

### 11.7 Snapshot Creation Flow

```
create_snapshot:
    1. Pause all sources (stop processing changes, sources retain position).
    2. Record current position for each source.
    3. For each pipeline:
        - Call target.export_snapshot().
        - Write through snapshot serializer to snapshot store.
    4. Write metadata (positions, table info, user_meta).
    5. Resume all sources.
```

**Note**: For now the engine always pauses during snapshot, even if the target reports `supports_consistent_snapshot() == true`. This is reserved for future optimization.

### 11.8 Scheduling and Retention

Snapshots can be triggered:

- **Manually** via gRPC/CLI.
- **Periodically** on a configurable interval.
- **On shutdown** — graceful shutdown takes a snapshot before exiting.

```hocon
snapshot {
  enabled = true
  store = s3
  store_config {
    bucket = my-bucket
    prefix = "snapshots/"
    region = us-east-1
  }
  serializer = jsonl
  schedule = "every 6h"
  on_shutdown = true
  retention {
    keep_count = 10
    max_age = 7d
  }
  user_meta {
    config_version = "2.1"
    service_id = warden-01
  }
  on_restore_mismatch = warn    # warn | reject
}
```

---

## 12. Validation and Consistency Checks

```hocon
validation {
  post_baseline_row_count = true
  periodic_row_count {
    enabled = true
    interval = 1h
  }
  on_mismatch = warn            # warn | re_baseline | error
}
```

After a baseline or snapshot restore, optionally run a `SELECT count(*)` against the source and compare against the target's row count. Can also run periodically for ongoing drift detection. Exposed through observer callbacks.

---

## 13. Observability — EngineObserver Interface

The core library does not log or emit metrics directly. It publishes structured events through an observer interface. The implementation decides how to generate metrics, logs, traces, or anything else.

```
interface EngineObserver {

    // Lifecycle
    onSourceConnected(sourceId: String, sourceType: String)
    onSourceDisconnected(sourceId: String, reason: String)
    onPipelineStateChanged(pipelineId: String, oldState: PipelineState, newState: PipelineState)

    // Baseline
    onBaselineStarted(pipelineId: String, table: TableIdentifier)
    onBaselineRowLoaded(pipelineId: String, table: TableIdentifier, rowCount: long)
    onBaselineCompleted(pipelineId: String, table: TableIdentifier, totalRows: long, duration: Duration)

    // Streaming
    onChangeReceived(pipelineId: String, table: TableIdentifier, action: ChangeAction, position: Position)
    onChangeApplied(pipelineId: String, table: TableIdentifier, action: ChangeAction, duration: Duration)
    onChangeError(pipelineId: String, table: TableIdentifier, action: ChangeAction, error: ErrorInfo)

    // ACK
    onAckAdvanced(sourceId: String, position: Position)

    // Backpressure
    onBufferDepthChanged(pipelineId: String, depth: int, capacity: int)
    onBufferPolicyTriggered(pipelineId: String, policy: BufferPolicy)

    // Snapshots
    onSnapshotStarted(snapshotId: String)
    onSnapshotCompleted(snapshotId: String, tables: int, rows: long, sizeBytes: long, duration: Duration)
    onSnapshotFailed(snapshotId: String, error: ErrorInfo)
    onSnapshotRestoreStarted(snapshotId: String)
    onSnapshotRestoreCompleted(snapshotId: String, duration: Duration)

    // Schema
    onSchemaChange(sourceId: String, table: TableIdentifier, change: SchemaChangeEvent)

    // Lag
    onLagUpdated(sourceId: String, lagBytes: long, lagTime: Duration?)

    // Dead letters
    onDeadLetterWritten(pipelineId: String, change: ChangeEvent, error: ErrorInfo)

    // TTL
    onRowExpired(pipelineId: String, table: TableIdentifier, key: String)

    // Validation
    onValidationResult(pipelineId: String, table: TableIdentifier,
                       sourceCount: long, targetCount: long, match: bool)

    // Replication fan-out
    onFanOutClientConnected(pipelineId: String, clientId: String, syncMode: SyncMode)
    onFanOutClientDisconnected(pipelineId: String, clientId: String, reason: String)
    onFanOutClientCaughtUp(pipelineId: String, clientId: String, sequence: long)
    onFanOutClientBackpressure(pipelineId: String, clientId: String, bufferDepth: int)
    onFanOutSnapshotCreated(pipelineId: String, snapshotId: String, sequence: long, rows: long)
    onFanOutJournalPruned(pipelineId: String, entriesPruned: long, oldestSequence: long)
}
```

The pre-built service ships with `PrometheusObserver` and `OpenTelemetryObserver`. Embedded users implement whatever they want or use a `CompositeObserver` to fan out to multiple observers.

---

## 14. Readiness Signaling

### 14.1 Library API

```
engine.awaitReady(timeout: Duration) -> bool
engine.isReady() -> bool
engine.isReady(sourceId: String) -> bool
engine.isReady(sourceId: String, table: TableIdentifier) -> bool
engine.isReady(pipelineId: String) -> bool

// Callback style
engine.onReady(() -> startAcceptingTraffic())
engine.onReady(sourceId, table, () -> enableFeature())
```

Readiness means: baseline complete (or snapshot restored) and streaming. A pipeline in ERROR is not ready. Global `isReady()` returns `true` only when **all** pipelines are ready.

### 14.2 Pre-built Service

The pre-built service exposes an HTTP `/health/ready` endpoint (standard Kubernetes readiness probe) that delegates to `engine.isReady()`.

---

## 15. Replay (Debugging)

A replay engine reads a snapshot and plays it through a target as if it were a live source. No actual source connection needed.

### 15.1 Library API

```
replay = SnapshotReplay.builder()
    .snapshotStore(s3SnapshotStore)
    .snapshotId("snap_20260320_123456")
    .target(myNewTarget)
    .speed(ReplaySpeed.FULL_SPEED)          // or REAL_TIME, or throttled
    .filter(table("public", "config_document"))
    .observer(myObserver)
    .build()

replay.run()        // blocking
// or
replay.start()      // async
replay.awaitComplete(timeout)
```

### 15.2 Use Cases

- Debugging target behavior against production data.
- Testing a new compiler/transform without touching the live stream.
- Benchmarking target performance.
- Validating a new target implementation before going live.

---

## 16. Library Builder API

Embedded users configure the engine programmatically in Go:

```go
engine, errs := tablesync.NewEngine(
    tablesync.WithSource("pg_main", pgsource.New(
        pgsource.Connection("postgresql://..."),
        pgsource.SlotMode(pgsource.Stateful),
        pgsource.SlotName("tablesync_warden_01"),
        pgsource.Publication(
            pgsource.PubName("tablesync_warden_01_pub"),
            pgsource.PubCreate(true),
            pgsource.PubPublish("insert", "update", "delete", "truncate"),
            pgsource.PubTableFilter("public.config_document", "status = 'active'"),
            pgsource.PubTableColumns("public.rules", "instance_id", "key", "data", "updated_at"),
        ),
        pgsource.Reconnect(10, 1*time.Second, 60*time.Second, 2.0),
    )),

    tablesync.WithSource("kinesis_events", kinesissource.New(
        kinesissource.BaselineBucket("s3://snapshots/events/"),
        kinesissource.StreamARN("arn:aws:kinesis:..."),
        kinesissource.ConsumerGroup("tablesync-warden-01"),
    )),

    // Pipelines (source -> table -> target)
    tablesync.WithPipeline("pg_main", tablesync.Table("public", "config_document"),
        memory.NewIndexedTarget(
            memory.LookupFields("instance_id", "key"),
            memory.AddIndex("by_instance", []string{"instance_id"}, false),
        ),
        tablesync.PipelineFilter(func(table tablesync.TableID, row tablesync.Row) bool {
            return row.GetString("status") == "active"
        }),
        tablesync.PipelineTransform(func(table tablesync.TableID, row tablesync.Row) tablesync.Row {
            return row.Without("ssn", "internal_notes")
        }),
        tablesync.BufferSize(1000),
        tablesync.BufferPolicy(tablesync.Block),
        tablesync.ErrorPolicy(tablesync.Isolate),
        tablesync.MaxRetries(5),
    ),

    tablesync.WithPipeline("pg_main", tablesync.Table("public", "config_document"),
        httptarget.New(
            httptarget.BaseURL("https://downstream.example.com/api/sync"),
            httptarget.BatchSize(500),
            httptarget.FlushInterval(200*time.Millisecond),
        ),
    ),

    tablesync.WithPipeline("pg_main", tablesync.Table("public", "rules"),
        memory.NewCompiledTarget(
            memory.Compiler(rulesetCompiler),
            memory.KeyFields("instance_id", "key"),
        ),
    ),

    tablesync.WithPipeline("kinesis_events", tablesync.Table("events", "user_activity"),
        httptarget.New(
            httptarget.BaseURL("https://analytics.example.com/ingest"),
        ),
    ),

    // Snapshots (optional)
    tablesync.WithSnapshotStore(s3snapshot.New(
        s3snapshot.Bucket("my-bucket"),
        s3snapshot.Prefix("snapshots/"),
    )),
    tablesync.WithSnapshotSerializer(jsonlserializer.New()),
    tablesync.WithSnapshotSchedule(6*time.Hour),
    tablesync.WithSnapshotOnShutdown(true),

    // Observability (optional)
    tablesync.WithObserver(myCustomObserver),
)
if len(errs) > 0 {
    log.Fatalf("config errors: %v", errs)
}

// Start — runs baseline/restore + streaming in background goroutines
engine.Start()

// Access targets directly for querying
configDocs := tablesync.GetTarget[*memory.IndexedTarget](engine, "pg_main", "public.config_document")
row, ok := configDocs.Lookup("inst_abc", "rulesets/default")

// Subscribe to changes
configDocs.Listen(func(old, new tablesync.Row) {
    // react to changes
})

// Admin operations
engine.Reload("pg_main", tablesync.Table("public", "config_document"))
engine.Pause("pg_main")
engine.Resume("pg_main")
engine.CreateSnapshot(map[string]any{"config_version": "2.1"})

// Readiness
engine.AwaitReady(30 * time.Second)

// Shutdown
engine.Stop()   // takes snapshot if configured, then clean shutdown
```

### 16.1 Optional gRPC Attachment

```go
engine, _ := tablesync.NewEngine(/* ... */)
engine.Start()

// Optional: attach gRPC management
grpcServer := tablesyncgrpc.NewServer(engine,
    tablesyncgrpc.Port(4001),
    tablesyncgrpc.EnableOAM(true),
    tablesyncgrpc.EnableQuery(true),
)
grpcServer.Start()
```

### 16.2 Custom Source/Target

```go
tablesync.WithPipeline("pg_main", tablesync.Table("public", "rules"),
    &myCustomTarget{},  // implements tablesync.SyncTarget interface
)
```

---

## 17. gRPC Service

The service exposes a single gRPC server with two service definitions: **OAM** (operations, administration, monitoring) and **Query**.

### 17.1 OAM Service

```protobuf
syntax = "proto3";
package tablesync.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/struct.proto";

service TableSyncOAM {

  // --- Status & Monitoring ---

  rpc GetStatus(GetStatusRequest) returns (GetStatusResponse);
  rpc GetTableStatus(GetTableStatusRequest) returns (GetTableStatusResponse);
  rpc GetPipelineStatus(GetPipelineStatusRequest) returns (GetPipelineStatusResponse);
  rpc WatchStatus(WatchStatusRequest) returns (stream StatusEvent);
  rpc CheckReady(CheckReadyRequest) returns (CheckReadyResponse);

  // --- Source Management ---

  rpc GetSourceInfo(GetSourceInfoRequest) returns (GetSourceInfoResponse);

  // --- Administration ---

  rpc ReloadTable(ReloadTableRequest) returns (ReloadTableResponse);
  rpc ReloadAll(ReloadAllRequest) returns (ReloadAllResponse);
  rpc PauseSync(PauseSyncRequest) returns (PauseSyncResponse);
  rpc ResumeSync(ResumeSyncRequest) returns (ResumeSyncResponse);
  rpc ResetSource(ResetSourceRequest) returns (ResetSourceResponse);

  // --- Configuration (read-only) ---

  rpc ListTables(ListTablesRequest) returns (ListTablesResponse);
  rpc GetTableSchema(GetTableSchemaRequest) returns (GetTableSchemaResponse);

  // --- Snapshot Management ---

  rpc CreateSnapshot(CreateSnapshotRequest) returns (CreateSnapshotResponse);
  rpc ListSnapshots(ListSnapshotsRequest) returns (ListSnapshotsResponse);
  rpc InspectSnapshot(InspectSnapshotRequest) returns (InspectSnapshotResponse);
  rpc RestoreSnapshot(RestoreSnapshotRequest) returns (RestoreSnapshotResponse);
  rpc DeleteSnapshot(DeleteSnapshotRequest) returns (DeleteSnapshotResponse);
  rpc PruneSnapshots(PruneSnapshotsRequest) returns (PruneSnapshotsResponse);

  // --- Dead Letter Management ---

  rpc ListDeadLetters(ListDeadLettersRequest) returns (ListDeadLettersResponse);
  rpc ReplayDeadLetters(ReplayDeadLettersRequest) returns (ReplayDeadLettersResponse);
  rpc PurgeDeadLetters(PurgeDeadLettersRequest) returns (PurgeDeadLettersResponse);

  // --- Replay (Debugging) ---

  rpc StartReplay(StartReplayRequest) returns (StartReplayResponse);
  rpc GetReplayStatus(GetReplayStatusRequest) returns (GetReplayStatusResponse);
  rpc StopReplay(StopReplayRequest) returns (StopReplayResponse);
}

// ============================================================
// Status & Monitoring
// ============================================================

message GetStatusRequest {}

message GetStatusResponse {
  ServiceState state = 1;
  repeated SourceStatus sources = 2;
  repeated PipelineStatus pipelines = 3;
  google.protobuf.Timestamp started_at = 4;
  google.protobuf.Timestamp last_change_at = 5;
  SnapshotSummary snapshot_status = 6;
}

enum ServiceState {
  SERVICE_STATE_UNSPECIFIED = 0;
  SERVICE_STATE_STARTING = 1;
  SERVICE_STATE_BASELINING = 2;
  SERVICE_STATE_STREAMING = 3;
  SERVICE_STATE_PAUSED = 4;
  SERVICE_STATE_RELOADING = 5;
  SERVICE_STATE_ERROR = 6;
}

message SourceStatus {
  string source_id = 1;
  string source_type = 2;
  bool supports_resume = 3;
  string ordering_guarantee = 4;
  string current_position = 5;
  string acked_position = 6;
  int64 lag_bytes = 7;
  google.protobuf.Duration lag_time = 8;
  google.protobuf.Struct source_details = 9;    // source-specific metadata
}

message PipelineStatus {
  string pipeline_id = 1;
  string source_id = 2;
  string schema = 3;
  string table = 4;
  string target_type = 5;
  PipelineState state = 6;
  int64 row_count = 7;
  int64 inserts_applied = 8;
  int64 updates_applied = 9;
  int64 deletes_applied = 10;
  int64 truncates_applied = 11;
  google.protobuf.Timestamp last_change_at = 12;
  string error_message = 13;
  int32 buffer_depth = 14;
  int32 buffer_capacity = 15;
  int64 dead_letter_count = 16;
}

enum PipelineState {
  PIPELINE_STATE_UNSPECIFIED = 0;
  PIPELINE_STATE_INITIALIZING = 1;
  PIPELINE_STATE_BASELINING = 2;
  PIPELINE_STATE_STREAMING = 3;
  PIPELINE_STATE_PAUSED = 4;
  PIPELINE_STATE_ERROR = 5;
  PIPELINE_STATE_STOPPED = 6;
}

message GetTableStatusRequest {
  string schema = 1;
  string table = 2;
}

message GetTableStatusResponse {
  repeated PipelineStatus pipelines = 1;
  repeated IndexInfo indexes = 2;
}

message IndexInfo {
  string name = 1;
  repeated string fields = 2;
  bool unique = 3;
  int64 entry_count = 4;
}

message GetPipelineStatusRequest {
  string pipeline_id = 1;
}

message GetPipelineStatusResponse {
  PipelineStatus status = 1;
  repeated IndexInfo indexes = 2;
}

message WatchStatusRequest {
  repeated string tables = 1;         // "schema.table" format; empty = all
  repeated string pipeline_ids = 2;   // specific pipelines; empty = all
}

message StatusEvent {
  google.protobuf.Timestamp timestamp = 1;
  oneof event {
    ServiceStateChange service_state_change = 2;
    PipelineStateChangeEvent pipeline_state_change = 3;
    RowChange row_change = 4;
    SourceStateChange source_state_change = 5;
  }
}

message ServiceStateChange {
  ServiceState old_state = 1;
  ServiceState new_state = 2;
}

message PipelineStateChangeEvent {
  string pipeline_id = 1;
  PipelineState old_state = 2;
  PipelineState new_state = 3;
}

message SourceStateChange {
  string source_id = 1;
  string event_type = 2;       // "connected", "disconnected", "error", "lag_warning"
  string message = 3;
}

message RowChange {
  string pipeline_id = 1;
  string schema = 2;
  string table = 3;
  string action = 4;           // INSERT, UPDATE, DELETE, TRUNCATE
  string position = 5;
  int64 xid = 6;
}

message CheckReadyRequest {
  string source = 1;           // optional
  string table = 2;            // optional, "schema.table"
  string pipeline_id = 3;      // optional
}

message CheckReadyResponse {
  bool ready = 1;
  repeated string not_ready_reasons = 2;
}

// ============================================================
// Source Management
// ============================================================

message GetSourceInfoRequest {
  string source_id = 1;        // empty = all sources
}

message GetSourceInfoResponse {
  repeated SourceInfo sources = 1;
}

message SourceInfo {
  string source_id = 1;
  string source_type = 2;
  bool supports_resume = 3;
  string ordering_guarantee = 4;
  string current_position = 5;
  string acked_position = 6;
  int64 lag_bytes = 7;
  google.protobuf.Duration lag_time = 8;
  google.protobuf.Struct details = 9;       // source-specific
}

// ============================================================
// Administration
// ============================================================

message ReloadTableRequest {
  string source_id = 1;
  string schema = 2;
  string table = 3;
}

message ReloadTableResponse {
  bool accepted = 1;
  string message = 2;
}

message ReloadAllRequest {
  string source_id = 1;        // optional: reload all tables on this source. Empty = all sources.
}

message ReloadAllResponse {
  bool accepted = 1;
  string message = 2;
}

message PauseSyncRequest {
  string source_id = 1;        // optional: pause specific source. Empty = all.
}

message PauseSyncResponse {
  ServiceState state = 1;
}

message ResumeSyncRequest {
  string source_id = 1;
}

message ResumeSyncResponse {
  ServiceState state = 1;
}

message ResetSourceRequest {
  string source_id = 1;
  bool confirm = 2;
  bool drop_publication = 3;      // if true, drop the publication without recreating (for decommissioning)
}

message ResetSourceResponse {
  bool accepted = 1;
  string message = 2;
  google.protobuf.Struct details = 3;
}

// ============================================================
// Configuration (read-only)
// ============================================================

message ListTablesRequest {}

message ListTablesResponse {
  repeated TableConfig tables = 1;
}

message TableConfig {
  string schema = 1;
  string table = 2;
  string primary_key = 3;
  string source_id = 4;
  string source_type = 5;
  google.protobuf.Struct source_config = 6;
  string target_type = 7;
  google.protobuf.Struct target_config = 8;
}

message GetTableSchemaRequest {
  string schema = 1;
  string table = 2;
}

message GetTableSchemaResponse {
  repeated ColumnDefinition columns = 1;
}

message ColumnDefinition {
  int32 ordinal_position = 1;
  string column_name = 2;
  string data_type = 3;
  bool not_null = 4;
  bool is_primary_key = 5;
  int32 primary_key_ordinal = 6;
}

// ============================================================
// Snapshot Management
// ============================================================

message CreateSnapshotRequest {
  repeated string tables = 1;                   // empty = all
  google.protobuf.Struct user_meta = 2;
}

message CreateSnapshotResponse {
  bool accepted = 1;
  string snapshot_id = 2;
  string message = 3;
}

message ListSnapshotsRequest {
  string table = 1;
  int32 limit = 2;
}

message ListSnapshotsResponse {
  repeated SnapshotInfo snapshots = 1;
}

message SnapshotInfo {
  string snapshot_id = 1;
  google.protobuf.Timestamp created_at = 2;
  string format = 3;
  repeated SourcePositionEntry source_positions = 4;
  repeated TableSnapshotSummary tables = 5;
  int64 size_bytes = 6;
  string store_type = 7;
  string store_location = 8;
  google.protobuf.Struct user_meta = 9;
}

message SourcePositionEntry {
  string source_id = 1;
  string position = 2;
}

message TableSnapshotSummary {
  string schema = 1;
  string table = 2;
  int64 row_count = 3;
  string target_type = 4;
}

message InspectSnapshotRequest {
  string snapshot_id = 1;
}

message InspectSnapshotResponse {
  SnapshotInfo info = 1;
}

message RestoreSnapshotRequest {
  string snapshot_id = 1;
  bool confirm = 2;
  bool skip_meta_check = 3;
}

message RestoreSnapshotResponse {
  bool accepted = 1;
  string message = 2;
  google.protobuf.Struct user_meta = 3;
  repeated string warnings = 4;
}

message DeleteSnapshotRequest {
  string snapshot_id = 1;
}

message DeleteSnapshotResponse {
  bool deleted = 1;
}

message PruneSnapshotsRequest {
  int32 keep = 1;
  string table = 2;
}

message PruneSnapshotsResponse {
  int32 deleted_count = 1;
}

message SnapshotSummary {
  bool enabled = 1;
  string store_type = 2;
  string last_snapshot_id = 3;
  google.protobuf.Timestamp last_snapshot_at = 4;
  google.protobuf.Timestamp next_scheduled_at = 5;
  int32 snapshot_count = 6;
}

// ============================================================
// Dead Letter Management
// ============================================================

message ListDeadLettersRequest {
  string pipeline_id = 1;
  int32 limit = 2;
}

message ListDeadLettersResponse {
  repeated DeadLetterEntry entries = 1;
  int32 total_count = 2;
}

message DeadLetterEntry {
  google.protobuf.Timestamp timestamp = 1;
  string action = 2;
  string position = 3;
  string error_message = 4;
  google.protobuf.Struct change_data = 5;
}

message ReplayDeadLettersRequest {
  string pipeline_id = 1;
}

message ReplayDeadLettersResponse {
  int32 replayed = 1;
  int32 succeeded = 2;
  int32 failed = 3;
}

message PurgeDeadLettersRequest {
  string pipeline_id = 1;
}

message PurgeDeadLettersResponse {
  int32 purged = 1;
}

// ============================================================
// Replay (Debugging)
// ============================================================

message StartReplayRequest {
  string snapshot_id = 1;
  string target_pipeline_id = 2;
  string speed = 3;                    // "full", "realtime", "10x"
  repeated string tables = 4;
}

message StartReplayResponse {
  bool accepted = 1;
  string replay_id = 2;
  string message = 3;
}

message GetReplayStatusRequest {
  string replay_id = 1;
}

message GetReplayStatusResponse {
  string replay_id = 1;
  string state = 2;                    // "running", "completed", "stopped", "error"
  int64 rows_replayed = 3;
  google.protobuf.Duration elapsed = 4;
}

message StopReplayRequest {
  string replay_id = 1;
}

message StopReplayResponse {
  bool stopped = 1;
}
```

### 17.2 Query Service

```protobuf
service TableSyncQuery {

  rpc Lookup(LookupRequest) returns (LookupResponse);
  rpc LookupAll(LookupAllRequest) returns (LookupAllResponse);
  rpc GetRow(GetRowRequest) returns (GetRowResponse);
  rpc ListRows(ListRowsRequest) returns (ListRowsResponse);
  rpc CountRows(CountRowsRequest) returns (CountRowsResponse);
  rpc Subscribe(SubscribeRequest) returns (stream ChangeEvent);
}

message LookupRequest {
  string schema = 1;
  string table = 2;
  string index_name = 3;
  repeated google.protobuf.Value key_values = 4;
}

message LookupResponse {
  bool found = 1;
  google.protobuf.Struct row = 2;
}

message LookupAllRequest {
  string schema = 1;
  string table = 2;
  string index_name = 3;
  repeated google.protobuf.Value key_values = 4;
}

message LookupAllResponse {
  repeated google.protobuf.Struct rows = 1;
}

message GetRowRequest {
  string schema = 1;
  string table = 2;
  int64 primary_key = 3;
}

message GetRowResponse {
  bool found = 1;
  google.protobuf.Struct row = 2;
}

message ListRowsRequest {
  string schema = 1;
  string table = 2;
  int32 page_size = 3;
  string page_token = 4;
}

message ListRowsResponse {
  repeated google.protobuf.Struct rows = 1;
  string next_page_token = 2;
  int64 total_count = 3;
}

message CountRowsRequest {
  string schema = 1;
  string table = 2;
}

message CountRowsResponse {
  int64 count = 1;
}

message SubscribeRequest {
  string schema = 1;
  string table = 2;
  bool replay_existing = 3;
}

message ChangeEvent {
  string action = 1;
  string position = 2;
  int64 xid = 3;
  google.protobuf.Timestamp timestamp = 4;
  google.protobuf.Struct old_values = 5;
  google.protobuf.Struct new_values = 6;
}
```

---

## 18. Go CLI Tool (`tsync`)

A single Go binary that speaks gRPC to the table sync service.

### 18.1 Global Flags

```
--address, -a    gRPC server address (default: localhost:4001, env: TSYNC_ADDRESS)
--tls            enable TLS (default: false)
--cert           path to TLS cert
--timeout        request timeout (default: 10s)
--output, -o     output format: "table", "json", "yaml" (default: "table")
```

### 18.2 `tsync status`

```
$ tsync status

Service State:  STREAMING
Uptime:         3h 42m

SOURCE: pg_main (postgresql, stateful, resumable, ordering=total)
  Position: 0/1A2B3C4D    ACKed: 0/1A2B3C40    Lag: 1.2 KB

  PIPELINE                                          STATE       ROWS     BUFFER     LAST CHANGE
  public.config_document -> indexed-memory          STREAMING   1,284    0/1000     12s ago
  public.config_document -> http-sync               STREAMING   —        12/1000    12s ago
  public.config_document -> replication-fanout       STREAMING   1,284    —          12s ago    [12 clients]
  public.rules -> compiled-memory                   STREAMING   312      0/1000     4m ago
  public.audit_log -> http-sync                     ERROR       —        1000/1000  2m ago

SOURCE: kinesis_events (s3-kinesis, resumable, ordering=per-partition)
  Position: shard-000:496...    ACKed: shard-000:496...    Lag: 3.4s

  PIPELINE                                          STATE       ROWS     BUFFER     LAST CHANGE
  events.user_activity -> http-sync                 STREAMING   —        3/1000     1s ago

Snapshots: enabled (s3), last: snap_20260320_063000 (6h ago), next: in 12m
```

```
$ tsync status --table public.config_document

Table:          public.config_document
Source:         pg_main

PIPELINE: public.config_document -> indexed-memory
  State:          STREAMING
  Row Count:      1,284
  Inserts:        42
  Updates:        7
  Deletes:        1
  Truncates:      0
  Buffer:         0/1000
  Last Change:    2026-03-20T12:34:44Z

  INDEXES:
  NAME               FIELDS                    UNIQUE   ENTRIES
  (primary)          instance_id, key          yes      1,284
  by_instance        instance_id               no       1,284

PIPELINE: public.config_document -> http-sync
  State:          STREAMING
  Buffer:         12/1000
  Last Change:    2026-03-20T12:34:44Z
```

### 18.3 `tsync source [source_id]`

```
$ tsync source pg_main

Source ID:           pg_main
Source Type:         postgresql
Resumable:           yes
Ordering:            total (commit order across all tables)
Position:            0/1A2B3C4D
ACKed Position:      0/1A2B3C40
Lag:                 1.2 KB

PostgreSQL Details:
  Slot Name:         tablesync_warden_01
  Slot Type:         persistent
  Plugin:            pgoutput
  Active:            true
  Restart LSN:       0/1A2B3000
  Confirmed Flush:   0/1A2B3C40
  Created:           2026-03-18T09:00:00Z

  Publication:       tablesync_warden_01_pub
  Auto-managed:      yes
  Publish:           insert, update, delete, truncate
  Filter Push-down:  enabled (PG 15+)
  Published Tables:  public.config_document, public.rules, public.audit_log
```

```
$ tsync source kinesis_events

Source ID:           kinesis_events
Source Type:         s3-kinesis
Resumable:           yes
Ordering:            per-partition
Position:            shard-000:49607...123, shard-001:49607...456
ACKed Position:      shard-000:49607...120, shard-001:49607...450
Lag:                 3.4s (estimated)

Kinesis Details:
  Stream ARN:        arn:aws:kinesis:us-east-1:123456:stream/cdc-events
  Consumer Group:    tablesync-warden-01
  Shards:            2

  SHARD              SEQUENCE                    BEHIND     LAST EVENT
  shard-000          49607...123                 1.2s       12:34:44Z
  shard-001          49607...456                 3.4s       12:34:41Z
```

### 18.4 `tsync pipelines`

```
$ tsync pipelines

SOURCE         TABLE                      TARGET               STATE       BUFFER      ERRORS   DLQ
pg_main        public.config_document     indexed-memory       STREAMING   0/1000      0        0
pg_main        public.config_document     http-sync            STREAMING   12/1000     0        0
pg_main        public.config_document     replication-fanout   STREAMING   —           0        0
pg_main        public.rules               compiled-memory      STREAMING   0/1000      0        0
pg_main        public.audit_log           http-sync            ERROR       1000/1000   47       47
kinesis        events.user_activity       http-sync            STREAMING   3/1000      0        0
```

### 18.5 `tsync tables`

```
$ tsync tables

TABLE                      SOURCE     PK     TARGETS
public.config_document     pg_main    $id    indexed-memory, http-sync
public.rules               pg_main    $id    compiled-memory
public.audit_log           pg_main    $id    http-sync
events.user_activity       kinesis    id     http-sync
```

### 18.6 `tsync schema <schema.table>`

```
$ tsync schema public.config_document

#   COLUMN          TYPE              NOT NULL   PK
1   $id             bigint            yes        1
2   instance_id     text              yes
3   key             text              yes
4   data            jsonb             no
5   created_at      timestamptz       yes
6   updated_at      timestamptz       no
```

### 18.7 `tsync query <schema.table> [key_values...]`

```
# Lookup by primary lookup index
$ tsync query public.config_document inst_abc rulesets/default

$id:           42
instance_id:   inst_abc
key:           rulesets/default
data:          {"rules": [...]}
created_at:    2026-03-18T09:00:00Z
updated_at:    2026-03-20T12:34:44Z
```

```
# Lookup by named index (non-unique, returns multiple)
$ tsync query public.config_document --index by_instance inst_abc

Showing 3 rows:

$id   instance_id   key                    data         created_at                updated_at
42    inst_abc       rulesets/default       {...}        2026-03-18T09:00:00Z      2026-03-20T12:34:44Z
43    inst_abc       rulesets/custom        {...}        2026-03-18T09:01:00Z      2026-03-19T15:00:00Z
44    inst_abc       config/settings        {...}        2026-03-18T09:02:00Z      null
```

```
# Get by primary key
$ tsync query public.config_document --pk 42

$id:           42
instance_id:   inst_abc
...
```

```
# List all rows (paginated)
$ tsync query public.config_document --all --limit 10

Showing rows 1-10 of 1,284:
...
```

### 18.8 `tsync watch [schema.table]`

```
# Watch all status events
$ tsync watch

12:34:44.123  pg_main  public.config_document  INSERT   pos=0/1A2B3C4D  xid=12345
12:34:44.456  pg_main  public.config_document  UPDATE   pos=0/1A2B3C50  xid=12346
12:34:45.001  pg_main  public.audit_log        INSERT   pos=0/1A2B3C55  xid=12347

# Watch a specific table with full row data
$ tsync watch public.config_document --verbose

12:34:44.123  INSERT  pos=0/1A2B3C4D
  + instance_id: inst_xyz
  + key: rulesets/new
  + data: {"rules": [...]}

12:34:44.456  UPDATE  pos=0/1A2B3C50
  ~ instance_id: inst_abc  (unchanged)
  ~ key: rulesets/default  (unchanged)
  ~ data: {"rules": [... old ...]}  ->  {"rules": [... new ...]}
```

### 18.9 `tsync reload [schema.table]`

```
$ tsync reload public.config_document
Reload requested for public.config_document. Accepted.

$ tsync reload --all
Reload requested for all tables. Accepted.

$ tsync reload --source pg_main --all
Reload requested for all tables on source pg_main. Accepted.
```

### 18.10 `tsync pause` / `tsync resume`

```
$ tsync pause
Sync paused. WAL is accumulating.

$ tsync pause --source kinesis_events
Source kinesis_events paused.

$ tsync resume
Sync resumed. Catching up from paused position.
```

### 18.11 `tsync reset-source [source_id]`

```
$ tsync reset-source pg_main
WARNING: This will reset the source's position tracking and force a full re-baseline.
For postgresql: drops and recreates the replication slot and publication (if auto-managed).
All accumulated position tracking will be lost.
Type "yes" to confirm: yes

Source reset. Slot and publication recreated. Full re-baseline will begin on next resume.
```

```
# Drop the publication entirely (for decommissioning)
$ tsync reset-source pg_main --drop-publication
WARNING: This will drop the replication slot AND the publication.
The publication will NOT be recreated.
Type "yes" to confirm: yes

Source reset. Slot dropped. Publication tablesync_warden_01_pub dropped.
```

### 18.12 `tsync snapshot create`

```
$ tsync snapshot create --meta config_version=2.1 --meta service_id=warden-01
Snapshot requested. ID: snap_20260320_123456
Creating... done.
Tables: 3, Rows: 46,487, Size: 12.4 MB
Positions: pg_main=0/1A2B3C4D, kinesis_events=shard-000:496...
Stored: s3://my-bucket/snapshots/snap_20260320_123456/
```

### 18.13 `tsync snapshot list`

```
$ tsync snapshot list

ID                        CREATED              TABLES   ROWS     SIZE      STORE
snap_20260320_123456      2026-03-20 12:34     3        46,487   12.4 MB   s3
snap_20260320_063000      2026-03-20 06:30     3        45,102   11.9 MB   s3
snap_20260319_183000      2026-03-19 18:30     3        44,891   11.7 MB   s3
```

### 18.14 `tsync snapshot inspect <id>`

```
$ tsync snapshot inspect snap_20260320_123456

ID:          snap_20260320_123456
Created:     2026-03-20 12:34:56
Format:      jsonl
Size:        12.4 MB

Source Positions:
  pg_main:          0/1A2B3C4D
  kinesis_events:   shard-000:496...123, shard-001:496...456

User Metadata:
  config_version:  2.1
  service_id:      warden-01

TABLES:
TABLE                        ROWS      TARGET
public.config_document       1,284     indexed-memory
public.rules                 312       compiled-memory
events.user_activity         44,891    http-sync
```

### 18.15 `tsync snapshot restore <id>`

```
$ tsync snapshot restore snap_20260320_123456
Loading snapshot metadata...
User metadata check:
  config_version: 2.1 (matches current)
  service_id: warden-01 (matches current)
Proceed with restore? [yes/no]: yes

Restoring... done.
Loaded 46,487 rows across 3 tables.
Resuming streams from snapshot positions.
```

### 18.16 `tsync snapshot delete <id>` / `tsync snapshot prune`

```
$ tsync snapshot delete snap_20260319_183000
Deleted snapshot snap_20260319_183000.

$ tsync snapshot prune --keep 3
Deleted 12 snapshots. 3 remaining.
```

### 18.17 `tsync dead-letters <pipeline_id>`

```
$ tsync dead-letters pg_main:public.audit_log:http-sync

PIPELINE: pg_main:public.audit_log:http-sync
PENDING: 47

TIMESTAMP                ACTION   POSITION        ERROR
2026-03-20 12:34:44      INSERT   0/1A2B3C4D      HTTP 503: Service Unavailable
2026-03-20 12:34:44      INSERT   0/1A2B3C50      HTTP 503: Service Unavailable
...
```

```
$ tsync dead-letters replay pg_main:public.audit_log:http-sync
Replaying 47 dead letters... 45 succeeded, 2 failed.

$ tsync dead-letters purge pg_main:public.audit_log:http-sync
Purged 2 dead letters.
```

### 18.18 `tsync replay`

```
$ tsync replay snap_20260320_123456 --pipeline pg_main:public.config_document:indexed-memory --speed full
Replaying snapshot snap_20260320_123456...
  public.config_document: 1,284 rows loaded in 0.3s
Replay complete.

$ tsync replay snap_20260320_123456 --speed realtime
Replaying snapshot snap_20260320_123456 at real-time speed...
  public.config_document: 1,284 rows (estimated 4m remaining)
  ^C
Replay stopped.
```

### 18.19 `tsync ready`

```
$ tsync ready
READY: all 5 pipelines streaming.

$ tsync ready --pipeline pg_main:public.audit_log:http-sync
NOT READY: pipeline pg_main:public.audit_log:http-sync is in ERROR state.
```

Exit code 0 if ready, 1 if not. Useful in scripts and health checks.

### 18.20 `tsync fanout`

Commands for managing and inspecting the replication fan-out target.

```
$ tsync fanout status public.config_document

Replication Fan-Out: public.config_document
State:              STREAMING
gRPC Port:          4002
Current Sequence:   148,293
In-Memory Rows:     1,284

Journal:
  Entries:          42,103
  Oldest Sequence:  106,190  (2h 14m ago)
  Newest Sequence:  148,293  (now)

Snapshots:
  Latest:           snap_seq_145000  (12m ago, 1,284 rows, 2.1 MB)
  Available:        5

Connected Clients:  12

CLIENT ID                    SEQUENCE     BEHIND     STATE          BUFFER     CONNECTED
myapp-prod-a1b2c3            148,293      0          live           0/50000    3h 42m
myapp-prod-d4e5f6            148,293      0          live           0/50000    3h 42m
myapp-prod-g7h8i9            148,290      3          catching_up    3/50000    2s
myapp-canary-j0k1            148,100      193        catching_up    193/50000  1s
...
```

```
$ tsync fanout clients public.config_document

CLIENT ID                    SEQUENCE     BEHIND     STATE          BUFFER     CONNECTED
myapp-prod-a1b2c3            148,293      0          live           0/50000    3h 42m
myapp-prod-d4e5f6            148,293      0          live           0/50000    3h 42m
...
(12 clients total)
```

```
$ tsync fanout snapshots public.config_document

ID                    SEQUENCE    ROWS     SIZE      AGE
snap_seq_145000       145,000     1,284    2.1 MB    12m
snap_seq_140000       140,000     1,281    2.1 MB    37m
snap_seq_135000       135,000     1,279    2.0 MB    62m
snap_seq_130000       130,000     1,275    2.0 MB    87m
snap_seq_125000       125,000     1,270    2.0 MB    112m
```

```
$ tsync fanout journal public.config_document --tail 5

SEQUENCE    TIMESTAMP              ACTION   KEY
148,289     12:34:43.120           UPDATE   inst_abc:rulesets/default
148,290     12:34:43.455           INSERT   inst_xyz:rulesets/new
148,291     12:34:44.001           DELETE   inst_old:config/deprecated
148,292     12:34:44.123           UPDATE   inst_abc:config/settings
148,293     12:34:44.456           INSERT   inst_new:rulesets/default
```

---

## 19. Delivery and Consistency Guarantees

| Property | Source: supports_resume = false | Source: supports_resume = true |
|---|---|---|
| **Baseline-to-stream gap** | None (exported snapshot) | None (exported snapshot on first load; brief duplicate window on reload) |
| **Delivery semantics** | At-least-once (irrelevant — restart = full reload) | At-least-once (ACK deferred until `is_durable()` returns true on all targets) |
| **Restart behavior** | Full re-baseline | Resume from last ACKed position |
| **Position retention** | None after disconnect | Source retains from last ACK until reset |
| **Re-baseline** | Automatic on every start | On-demand via `ReloadTable`/`ReloadAll`, or automatic on position invalidation |

### 19.1 Ordering Guarantees by Source Type

| Source | Guarantee | Implication |
|---|---|---|
| PostgreSQL | `TOTAL_ORDER` | All changes across all tables in commit order. Engine delivers to targets in order received. |
| S3 + Kinesis | `PER_PARTITION_ORDER` | Ordered within a shard. No cross-shard guarantee. Engine may process shards concurrently. |
| SQL + Kafka | `PER_PARTITION_ORDER` | Ordered within a partition. No cross-partition guarantee. |

---

## 20. Full HOCON Configuration Reference

```hocon
// ============================================================
// Sources
// ============================================================

sources {
  pg_main {
    type = postgresql
    connection = "postgresql://user:pass@host:5432/dbname"
    slot_mode = stateful
    slot_name = tablesync_warden_01

    publication {
      name = tablesync_warden_01_pub
      create = true
      publish = "insert, update, delete, truncate"

      # Per-table publication parameters (PostgreSQL 15+)
      tables {
        "public.config_document" {
          where = "status = 'active'"
        }
        "public.rules" {
          columns = [instance_id, key, data, updated_at]
        }
      }
    }

    reconnect {
      max_attempts = 10
      initial_backoff = 1s
      max_backoff = 60s
      backoff_multiplier = 2.0
    }
  }

  kinesis_events {
    type = s3-kinesis
    baseline_bucket = "s3://my-bucket/snapshots/events/"
    baseline_format = jsonl
    stream_arn = "arn:aws:kinesis:us-east-1:123456:stream/cdc-events"
    consumer_group = tablesync-warden-01
    checkpoint_table = "dynamodb://tablesync-checkpoints"
    region = us-east-1
  }

  // mysql_main (sql-kafka) — deferred to future version
}

// ============================================================
// Tables and Pipelines
// ============================================================

tables = [
  {
    source = pg_main
    schema = public
    table = config_document

    targets = [
      {
        type = indexed-memory
        lookup_fields = [instance_id, key]
        additional_indexes = [
          { name = by_instance, fields = [instance_id], unique = false }
          { name = by_key_version, fields = [key, version], unique = true }
        ]

        buffer { max_size = 1000, policy = block }

        error_handling {
          max_retries = 5
          retry_backoff_ms = 1000
          retry_backoff_max_ms = 30000
          on_persistent_failure = isolate
        }
      }
      {
        type = http-sync
        base_url = "https://downstream.example.com/api/sync"
        batch_size = 500
        timeout_ms = 5000
        retry_count = 3
        retry_backoff_ms = 500
        auth_header = "Bearer ..."
        headers { X-Source = table-sync }

        filters = [
          { type = field-equals, field = status, value = active }
        ]
        transforms = [
          { type = drop-fields, fields = [ssn, internal_notes] }
          { type = add-timestamp, field = synced_at }
        ]

        buffer { max_size = 5000, policy = error }

        error_handling {
          max_retries = 10
          retry_backoff_ms = 500
          retry_backoff_max_ms = 60000
          on_persistent_failure = isolate
          dead_letter {
            enabled = true
            store = s3
            config {
              bucket = my-bucket
              prefix = "dead-letter/"
            }
          }
        }
      }
    ]

    ttl {
      mode = field
      field = expires_at
      check_interval = 30s
    }

    validation {
      post_baseline_row_count = true
      periodic_row_count { enabled = true, interval = 1h }
      on_mismatch = warn
    }
  }

  {
    source = pg_main
    schema = public
    table = rules

    targets = [
      {
        type = compiled-memory
        compiler = ruleset
        key_fields = [instance_id, key]
        filter { field = key, prefix = "rulesets/" }
      }
    ]
  }

  {
    source = kinesis_events
    schema = events
    table = user_activity

    targets = [
      {
        type = http-sync
        base_url = "https://analytics.example.com/ingest"
        batch_size = 200
        timeout_ms = 3000
      }
    ]
  }

  {
    source = pg_main
    schema = public
    table = config_document

    targets = [
      {
        type = replication-fanout

        journal {
          max_entries = 1000000
          max_age = 24h
        }

        snapshot {
          interval = 5m
          store = local
          store_config {
            path = "/var/lib/tablesync/fanout-snapshots"
          }
          serializer = jsonl
          retention { keep_count = 5, max_age = 1h }
        }

        grpc {
          port = 4002
          max_clients = 500
          tls { enabled = false }
        }

        client_buffer {
          max_size = 50000
          policy = drop_disconnect
        }

        heartbeat_interval = 5s
      }
    ]
  }
]

// ============================================================
// Snapshots
// ============================================================

snapshot {
  enabled = true
  store = s3
  store_config {
    bucket = my-bucket
    prefix = "snapshots/"
    region = us-east-1
  }
  serializer = jsonl
  schedule = "every 6h"
  on_shutdown = true
  retention {
    keep_count = 10
    max_age = 7d
  }
  user_meta {
    config_version = "2.1"
    service_id = warden-01
  }
  on_restore_mismatch = warn
}

// ============================================================
// gRPC Server
// ============================================================

grpc {
  port = 4001
  tls {
    enabled = false
    cert_path = ""
    key_path = ""
  }
}

// ============================================================
// Health endpoint
// ============================================================

health {
  http_port = 8080
  readiness_path = "/health/ready"
  liveness_path = "/health/live"
}

// ============================================================
// Observability
// ============================================================

observability {
  metrics {
    type = prometheus
    port = 9090
    path = "/metrics"
  }
  logging {
    type = structured
    level = info
  }
}
```

---

## 21. Docker Image

The pre-built service is published as a Docker image for easy deployment, testing, and orchestration.

### 21.1 Image Layout

```
ghcr.io/yourorg/tablesync:latest
ghcr.io/yourorg/tablesync:1.2.3

Base image: distroless/static (or alpine for debug variant)
Binary:     /usr/local/bin/tablesync-server
Config:     /etc/tablesync/tablesync.conf      (default config path)
Data:       /var/lib/tablesync/                 (local snapshots, dead letters)
```

### 21.2 Configuration Loading Order

The service resolves configuration in the following order (later sources override earlier):

1. **Built-in defaults** — sensible defaults for all settings.
2. **Config file** — loaded from the path specified by `--config` flag or `TABLESYNC_CONFIG` env var. Default: `/etc/tablesync/tablesync.conf`.
3. **Config directory** — all `*.conf` files in `/etc/tablesync/conf.d/` are loaded and merged (alphabetical order). Useful for splitting config across multiple ConfigMaps or volume mounts.
4. **Environment variables** — every config key can be overridden via env var. HOCON path maps to env var with dots replaced by underscores and uppercased: `sources.pg_main.connection` → `SOURCES_PG_MAIN_CONNECTION`. Nested objects can also use the `TABLESYNC_` prefix: `TABLESYNC_SOURCES_PG_MAIN_CONNECTION`.
5. **Command-line flags** — `--set key=value` for individual overrides.

This supports all common deployment patterns: baked-in config, volume-mounted config, ConfigMap-injected config, and env-var-only config.

### 21.3 Configuration Strategies

**Bake config into a derived image:**

```dockerfile
FROM ghcr.io/yourorg/tablesync:latest
COPY tablesync.conf /etc/tablesync/tablesync.conf
```

**Mount config via volume:**

```bash
docker run -v ./tablesync.conf:/etc/tablesync/tablesync.conf ghcr.io/yourorg/tablesync:latest
```

**Kubernetes ConfigMap:**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: tablesync-config
data:
  tablesync.conf: |
    sources {
      pg_main {
        type = postgresql
        connection = ${POSTGRES_URL}
        slot_mode = stateful
        slot_name = tablesync_warden_01
        publication {
          create = true
        }
      }
    }
    tables = [
      {
        source = pg_main
        schema = public
        table = config_document
        targets = [
          { type = indexed-memory, lookup_fields = [instance_id, key] }
        ]
      }
    ]
```

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tablesync
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: tablesync
          image: ghcr.io/yourorg/tablesync:latest
          ports:
            - name: grpc
              containerPort: 4001
            - name: health
              containerPort: 8080
            - name: metrics
              containerPort: 9090
          env:
            - name: POSTGRES_URL
              valueFrom:
                secretKeyRef:
                  name: tablesync-secrets
                  key: postgres-url
          volumeMounts:
            - name: config
              mountPath: /etc/tablesync
            - name: data
              mountPath: /var/lib/tablesync
          livenessProbe:
            httpGet:
              path: /health/live
              port: health
            initialDelaySeconds: 5
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /health/ready
              port: health
            initialDelaySeconds: 10
            periodSeconds: 5
          startupProbe:
            httpGet:
              path: /health/live
              port: health
            initialDelaySeconds: 5
            periodSeconds: 5
            failureThreshold: 60    # allow up to 5 minutes for initial baseline
      volumes:
        - name: config
          configMap:
            name: tablesync-config
        - name: data
          persistentVolumeClaim:
            claimName: tablesync-data
```

**Environment-variable-only (no config file):**

```bash
docker run \
  -e TABLESYNC_SOURCES_PG_MAIN_TYPE=postgresql \
  -e TABLESYNC_SOURCES_PG_MAIN_CONNECTION="postgresql://user:pass@host:5432/db" \
  -e TABLESYNC_SOURCES_PG_MAIN_SLOT_MODE=stateful \
  -e TABLESYNC_SOURCES_PG_MAIN_SLOT_NAME=tablesync_01 \
  ghcr.io/yourorg/tablesync:latest
```

### 21.4 Init Hooks

The image supports initialization hooks for config generation, secret injection, schema migration, or any other pre-start logic.

**Init script directory**: All executable files in `/docker-entrypoint-init.d/` are run (in alphabetical order) before the service starts. If any init script exits non-zero, the container fails to start.

```dockerfile
FROM ghcr.io/yourorg/tablesync:latest
COPY generate-config.sh /docker-entrypoint-init.d/01-generate-config.sh
COPY wait-for-pg.sh /docker-entrypoint-init.d/02-wait-for-pg.sh
```

Example init script — generate config from environment:

```bash
#!/bin/sh
# /docker-entrypoint-init.d/01-generate-config.sh
cat > /etc/tablesync/conf.d/generated.conf <<EOF
sources {
  pg_main {
    type = postgresql
    connection = "${POSTGRES_URL}"
    slot_mode = stateful
    slot_name = "tablesync_${SERVICE_ID}"
    publication {
      create = true
      name = "tablesync_${SERVICE_ID}_pub"
    }
  }
}
EOF
```

Example init script — wait for PostgreSQL to be ready:

```bash
#!/bin/sh
# /docker-entrypoint-init.d/02-wait-for-pg.sh
until pg_isready -h "$POSTGRES_HOST" -p "$POSTGRES_PORT"; do
  echo "Waiting for PostgreSQL..."
  sleep 2
done
```

**Kubernetes init containers** can also be used for the same purpose — the init hook mechanism is for simpler cases that don't need a separate container.

### 21.5 Entrypoint

```dockerfile
ENTRYPOINT ["tablesync-server"]
CMD ["--config", "/etc/tablesync/tablesync.conf"]
```

The entrypoint wrapper:

1. Runs all scripts in `/docker-entrypoint-init.d/` (if any).
2. Merges config from file + conf.d + env vars + CLI flags.
3. Validates the merged config (fails fast with clear error messages if invalid).
4. Starts the service.

Override the command for one-off operations:

```bash
# Validate config without starting
docker run ghcr.io/yourorg/tablesync:latest validate --config /etc/tablesync/tablesync.conf

# Dump merged config (for debugging)
docker run ghcr.io/yourorg/tablesync:latest config --dump

# Run CLI commands against a running instance
docker exec tablesync tsync status
```

### 21.6 Health Checks

The image includes a built-in Docker `HEALTHCHECK`:

```dockerfile
HEALTHCHECK --interval=10s --timeout=3s --start-period=120s --retries=3 \
  CMD ["tablesync-server", "healthcheck"]
```

The `healthcheck` subcommand hits the local HTTP health endpoint and exits 0/1. The `start-period` of 120s gives time for the initial baseline to complete without marking the container as unhealthy.

**Health endpoints** (configured in the `health` config block):

| Endpoint | Purpose | Returns 200 when |
|---|---|---|
| `/health/live` | Liveness | Process is running and not deadlocked. |
| `/health/ready` | Readiness | All pipelines have completed baseline and are streaming. |
| `/health/startup` | Startup | Service has initialized and begun baseline (may not be complete). |

**Readiness vs. liveness behavior:**

- A pipeline in `ERROR` state makes readiness fail but does not affect liveness (the process is still healthy, just one pipeline has a problem).
- If **all** pipelines on a source error out, liveness also fails (the source connection is likely broken).
- Readiness returns a JSON body with per-pipeline status for debugging:

```json
{
  "ready": false,
  "pipelines": {
    "pg_main:public.config_document:indexed-memory": "STREAMING",
    "pg_main:public.config_document:http-sync": "STREAMING",
    "pg_main:public.audit_log:http-sync": "ERROR: HTTP 503 after 5 retries"
  }
}
```

### 21.7 Exposed Ports

| Port | Purpose | Default | Config key |
|---|---|---|---|
| 4001 | gRPC (OAM + Query) | 4001 | `grpc.port` |
| 4002 | gRPC (Replication fan-out) | 4002 | per-target `grpc.port` |
| 8080 | Health HTTP | 8080 | `health.http_port` |
| 9090 | Metrics (Prometheus) | 9090 | `observability.metrics.port` |

### 21.8 Volumes

| Path | Purpose | Required |
|---|---|---|
| `/etc/tablesync/` | Config file(s) | No (can use env vars only) |
| `/etc/tablesync/conf.d/` | Additional config fragments | No |
| `/var/lib/tablesync/` | Local snapshot store, dead letter store, local state | Only if using `local` snapshot/dead-letter store |
| `/docker-entrypoint-init.d/` | Init hook scripts | No |

### 21.9 Graceful Shutdown

On `SIGTERM` (standard Docker/Kubernetes stop signal):

1. Stop accepting new gRPC connections.
2. Drain in-flight gRPC requests (configurable timeout, default 10s).
3. Take a snapshot if `snapshot.on_shutdown = true`.
4. Pause all sources.
5. Flush all pipeline buffers to targets.
6. ACK final positions to sources.
7. Close all source and target connections.
8. Exit 0.

The default Kubernetes `terminationGracePeriodSeconds` should be set long enough to allow the shutdown snapshot to complete. A reasonable default is 120s, but this depends on dataset size.

### 21.10 Docker Compose Example

```yaml
version: "3.8"

services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: mydb
      POSTGRES_USER: replicator
      POSTGRES_PASSWORD: secret
    command: >
      postgres
        -c wal_level=logical
        -c max_replication_slots=4
        -c max_wal_senders=4
    ports:
      - "5432:5432"

  tablesync:
    image: ghcr.io/yourorg/tablesync:latest
    depends_on:
      - postgres
    volumes:
      - ./tablesync.conf:/etc/tablesync/tablesync.conf
      - tablesync-data:/var/lib/tablesync
    ports:
      - "4001:4001"    # gRPC (OAM + Query)
      - "4002:4002"    # gRPC (Replication fan-out)
      - "8080:8080"    # health
      - "9090:9090"    # metrics
    environment:
      TABLESYNC_SOURCES_PG_MAIN_CONNECTION: "postgresql://replicator:secret@postgres:5432/mydb"
    healthcheck:
      test: ["CMD", "tablesync-server", "healthcheck"]
      interval: 10s
      timeout: 3s
      start_period: 120s
      retries: 3

volumes:
  tablesync-data:
```

---

## 22. Testing Strategy

Testing is a first-class concern. Every component is tested from day one, and tests are written alongside (or before) the implementation. The test suite has four tiers, each with a clear purpose and different tradeoffs between speed and realism.

### 22.1 Test Tiers

```
Tier 1: Unit tests              Fast, no I/O, no goroutines. Pure logic.
Tier 2: Integration tests       Test source + engine + targets together using the in-memory test source.
Tier 3: Testcontainers tests    Real PostgreSQL (and later Kinesis/Kafka) in Docker via testcontainers-go.
Tier 4: End-to-end tests        Full pre-built service in Docker with gRPC client, CLI, health checks.
```

All tiers run in CI. Tiers 1-2 run on every commit. Tier 3 runs on every PR and merge. Tier 4 runs on merge to main and release branches.

### 22.2 Tier 1: Unit Tests

Pure logic tests with no external dependencies. These cover:

- **Core types**: Position serialization/deserialization, TableIdentifier equality/hashing, Row field access, ColumnDefinition validation.
- **Pipeline filters**: Each built-in filter type tested with matching and non-matching rows.
- **Pipeline transforms**: Each built-in transform type tested with expected output.
- **Buffer policies**: Block, drop_oldest, error behavior with mock producers/consumers.
- **TTL logic**: Expiry calculation, expired-on-insert skip, expired-on-update delete semantics.
- **Journal**: Append, sequence numbering, pruning by count and age, oldest/newest sequence tracking.
- **Index structures**: BiMap, non-unique index insert/update/delete correctness. Composite key construction.
- **Snapshot serializer**: JSONL round-trip (write rows, read back, verify equality).
- **Position comparison**: Per-source position ordering (LSN comparison for PG, sequence comparison for Kinesis).
- **Config validation**: Structural validation catches missing fields, invalid ranges, port collisions.
- **HOCON parsing**: Config loading, env var override, conf.d merging.

```go
func TestFieldEqualsFilter(t *testing.T) {
    filter := filters.FieldEquals("status", "active")
    table := tablesync.TableID{Schema: "public", Table: "docs"}

    assert.True(t, filter.Include(table, tablesync.Row{"status": "active", "id": 1}))
    assert.False(t, filter.Include(table, tablesync.Row{"status": "archived", "id": 2}))
    assert.False(t, filter.Include(table, tablesync.Row{"id": 3}))  // missing field
}

func TestDropFieldsTransform(t *testing.T) {
    xform := transforms.DropFields("ssn", "internal_notes")
    table := tablesync.TableID{Schema: "public", Table: "docs"}

    row := tablesync.Row{"id": 1, "name": "Alice", "ssn": "123-45-6789", "internal_notes": "secret"}
    result := xform.Transform(table, row)

    assert.Equal(t, tablesync.Row{"id": 1, "name": "Alice"}, result)
}

func TestJournalPruning(t *testing.T) {
    j := journal.New(journal.MaxEntries(3))
    j.Append(makeEntry(1))
    j.Append(makeEntry(2))
    j.Append(makeEntry(3))
    j.Append(makeEntry(4))  // should prune entry 1

    assert.Equal(t, int64(2), j.OldestSequence())
    assert.Equal(t, int64(4), j.CurrentSequence())
    assert.Equal(t, 3, j.Len())
}
```

### 22.3 Tier 2: Integration Tests (Test Source)

Test the engine's orchestration, pipeline delivery, ACK coordination, error handling, snapshots, and schema evolution using the in-memory test source. No external dependencies — these tests are fast and deterministic.

#### 22.3.1 Test Source (`tablesync-source-test`)

A fully in-memory source for integration testing. Allows programmatic injection of rows and changes.

```go
source := testsource.New()

// Define tables and schema
source.AddTable("public", "config_document", []tablesync.ColumnDefinition{
    {Name: "$id", Type: "bigint", NotNull: true, IsPrimaryKey: true},
    {Name: "instance_id", Type: "text", NotNull: true},
    {Name: "key", Type: "text", NotNull: true},
    {Name: "data", Type: "jsonb"},
    {Name: "created_at", Type: "timestamptz", NotNull: true},
})

// Seed baseline rows
source.AddBaselineRow("public", "config_document", map[string]any{
    "$id": 1, "instance_id": "inst_abc", "key": "rulesets/default",
    "data": `{"rules": []}`, "created_at": time.Now(),
})
source.AddBaselineRow("public", "config_document", map[string]any{
    "$id": 2, "instance_id": "inst_abc", "key": "config/settings",
    "data": `{"theme": "dark"}`, "created_at": time.Now(),
})

// Use with engine
engine, _ := tablesync.NewEngine(
    tablesync.WithSource("test", source),
    tablesync.WithPipeline("test", tablesync.Table("public", "config_document"),
        indexedMemoryTarget),
)

engine.Start()
engine.AwaitReady(10 * time.Second)

// Inject changes while streaming
source.Insert("public", "config_document", map[string]any{
    "$id": 3, "instance_id": "inst_xyz", "key": "rulesets/new",
    "data": `{"rules": ["rule1"]}`, "created_at": time.Now(),
})
source.Update("public", "config_document",
    map[string]any{"$id": 1},  // identity
    map[string]any{"data": `{"rules": ["updated"]}`},  // changed columns
)
source.Delete("public", "config_document",
    map[string]any{"$id": 2},  // identity
)
source.Truncate("public", "config_document")

// Inject a schema change
source.AddColumn("public", "config_document",
    tablesync.ColumnDefinition{Name: "version", Type: "integer"})

// Control behavior
source.SetSupportsResume(true)
source.SetOrderingGuarantee(tablesync.TotalOrder)

// Simulate failures
source.InjectError(errors.New("connection lost"))  // triggers reconnect flow
source.SetLatency(50 * time.Millisecond)           // simulate slow source

// Assertions
assert.Equal(t, source.AckedPosition(), expectedPosition)
assert.Equal(t, source.BaselineCallCount(), 1)
```

**Test source properties:**

- `supports_resume()`: configurable, default `false`.
- `ordering_guarantee()`: configurable, default `TOTAL_ORDER`.
- `validate_tables()`: always succeeds unless configured to fail.
- Position type: simple monotonically increasing `int64`.
- `compare_positions()`: numeric comparison.
- Thread-safe: changes injected from test goroutines are delivered to the engine safely.

#### 22.3.2 Integration Test Coverage

Each of these is a test function (or a set of test functions) using the test source:

**Baseline:**
- Baseline loads all rows into indexed-memory target, verify by lookup.
- Baseline loads all rows into compiled-memory target with compiler, verify typed objects.
- Baseline with filter — only matching rows reach the target.
- Baseline with transform — rows are transformed before reaching the target.
- Baseline with multiple targets on the same table — all targets receive all rows.
- Baseline row count validation passes.

**Streaming:**
- INSERT delivered to target, verify new row appears.
- UPDATE delivered to target, verify row changed and listeners notified with old+new.
- DELETE delivered to target, verify row removed and listeners notified.
- TRUNCATE delivered to target, verify all rows cleared.
- Changes to multiple tables on the same source dispatch to the correct targets.
- Changes with filter — only matching changes reach the target.
- Changes with transform — changes are transformed.

**ACK coordination:**
- Single target: ACK advances after `is_durable()` returns true.
- Multiple targets (fan-out): ACK advances only after ALL targets confirm `is_durable()`.
- Slow target holds back ACK for all targets on the same source.
- HTTP target (mock): ACK only after mock HTTP response, not before.
- HTTP target with batching: ACK reflects last flushed batch position, not last buffered change.

**Error handling:**
- Pipeline error with `isolate` policy: errored pipeline stops, others continue.
- Pipeline error with `stop_source` policy: all pipelines on that source stop.
- Pipeline error with `stop_all` policy: entire engine stops.
- Dead letter capture: failed changes written to dead letter store.
- Retry with backoff: target fails N times, then succeeds, changes are delivered.
- Retry exhausted: pipeline enters ERROR state.

**Backpressure:**
- Buffer fills with `block` policy: source delivery blocks until buffer drains.
- Buffer fills with `drop_oldest` policy: oldest entries dropped, newest delivered.
- Buffer fills with `error` policy: pipeline enters ERROR state.

**Readiness:**
- `AwaitReady` blocks until baseline complete.
- `IsReady` returns false during baseline, true after.
- Per-pipeline readiness: one pipeline ready, another still baselining.
- Pipeline in ERROR: global readiness is false.

**Schema evolution:**
- Column added: target receives `on_schema_change`, continues, new rows include new column.
- Column removed (not indexed): target continues, new rows omit column.
- Column removed (indexed): target returns `RE_BASELINE`, engine triggers reload.
- Column type changed: target returns appropriate action.

**TTL:**
- Row with past expiry on insert: skipped.
- Row expires after insert: removed on next TTL check.
- Row updated with future expiry: stays alive.
- Row updated with past expiry: removed immediately.

**Snapshots:**
- Create snapshot: all pipeline states serialized, positions recorded.
- Restore snapshot: target state matches snapshot, streaming resumes from snapshot position.
- Snapshot with user metadata: metadata round-trips correctly.
- Snapshot restore with mismatched user metadata: warning emitted.

**Reconnect:**
- Source error injected: engine pauses pipelines, source reconnects, streaming resumes.
- Source error with `supports_resume = true`: no re-baseline after reconnect.
- Source error with `supports_resume = false`: re-baseline after reconnect.
- Max reconnect attempts exceeded: source enters ERROR, pipelines stop.

**Shutdown:**
- Graceful shutdown: all buffers flushed, positions ACKed, snapshot taken if configured.
- Shutdown ordering: fan-out clients disconnected before sources closed.

**Observer:**
- All observer callbacks fire at the expected times with correct arguments.
- Composite observer fans out to multiple observers.

### 22.4 Tier 3: Testcontainers Tests (Real PostgreSQL)

These tests use [testcontainers-go](https://github.com/testcontainers/testcontainers-go) to start a real PostgreSQL instance in Docker. They validate the PostgreSQL source implementation against actual logical replication behavior.

#### 22.4.1 Test Infrastructure

```go
package pgtest

import (
    "context"
    "testing"

    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
    "github.com/testcontainers/testcontainers-go/wait"
)

// SharedPostgres provides a PostgreSQL container that is started once
// and shared across all tests in the package. Tables are created per-test
// and cleaned up after.
type SharedPostgres struct {
    container *postgres.PostgresContainer
    connStr   string
}

func NewSharedPostgres(t *testing.T) *SharedPostgres {
    t.Helper()
    ctx := context.Background()

    container, err := postgres.Run(ctx,
        "postgres:16-alpine",
        postgres.WithDatabase("tablesync_test"),
        postgres.WithUsername("replicator"),
        postgres.WithPassword("test"),
        // Enable logical replication
        postgres.WithConfigFile(createPGConfig(t)),
        testcontainers.WithWaitStrategy(
            wait.ForLog("database system is ready to accept connections").
                WithOccurrence(2).  // wait for both startup messages
                WithStartupTimeout(60*time.Second),
        ),
    )
    require.NoError(t, err)

    connStr, err := container.ConnectionString(ctx, "sslmode=disable")
    require.NoError(t, err)

    t.Cleanup(func() {
        container.Terminate(ctx)
    })

    return &SharedPostgres{container: container, connStr: connStr}
}

// createPGConfig writes a postgresql.conf snippet enabling logical replication.
func createPGConfig(t *testing.T) string {
    t.Helper()
    // Write temp file with:
    //   wal_level = logical
    //   max_replication_slots = 10
    //   max_wal_senders = 10
    // Return path
}

// CreateTestTable creates a table for a specific test and returns a cleanup function.
func (pg *SharedPostgres) CreateTestTable(t *testing.T, schema, table string, ddl string) {
    t.Helper()
    db := pg.Connect(t)
    _, err := db.Exec(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", schema))
    require.NoError(t, err)
    _, err = db.Exec(ddl)
    require.NoError(t, err)

    t.Cleanup(func() {
        db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s.%s CASCADE", schema, table))
    })
}

// SeedRows inserts rows into a test table.
func (pg *SharedPostgres) SeedRows(t *testing.T, schema, table string, rows []map[string]any) {
    // ...
}

// InsertRow inserts a single row (for streaming change tests).
func (pg *SharedPostgres) InsertRow(t *testing.T, schema, table string, row map[string]any) {
    // ...
}

// UpdateRow, DeleteRow, TruncateTable, AlterTableAddColumn, etc.
```

#### 22.4.2 PostgreSQL Source Test Coverage

**Slot management (ephemeral):**
- Ephemeral slot created on start, dropped on close.
- After restart, slot is gone — full re-baseline occurs.
- Temporary slot does not survive container restart.

**Slot management (stateful):**
- Named slot created on first start.
- After clean shutdown and restart, slot exists — streaming resumes from last ACK.
- Slot lag is reported correctly.
- Slot invalidation (simulate `max_slot_wal_keep_size` exceeded): engine detects, drops slot, re-baselines.

**Baseline:**
- Full baseline matches `SELECT *` results exactly.
- Baseline with large table (10k+ rows): all rows delivered, count matches.
- Baseline snapshot consistency: insert rows during baseline, verify they appear in the stream after baseline (not during).
- Baseline with multiple tables: all tables baselined, correct rows in each target.

**Streaming:**
- INSERT in PostgreSQL → `on_insert` in target with correct column values.
- UPDATE in PostgreSQL → `on_update` in target with correct old identity and new values.
- DELETE in PostgreSQL → `on_delete` in target with correct identity.
- TRUNCATE in PostgreSQL → `on_truncate` in target.
- Multi-row transaction: all changes from a single transaction delivered in order.
- Rapid changes (1000 inserts in quick succession): all delivered, none lost.
- Large column values (JSONB with 1MB payload): delivered correctly.
- NULL values in columns: handled correctly.
- Various PostgreSQL types: text, integer, bigint, boolean, timestamptz, jsonb, uuid, bytea, arrays.

**WAL ACK:**
- In stateful mode: after target confirms `is_durable()`, slot's `confirmed_flush_lsn` advances.
- In stateful mode: if target does not confirm, `confirmed_flush_lsn` does not advance.
- After restart in stateful mode: changes since last ACK are redelivered.

**Publication management:**
- `create = true`: publication created if not exists.
- `create = true`: publication altered when table list changes.
- `create = false`: fails if publication doesn't exist.
- Publication with row filter (PG 15+): only matching rows appear in stream.
- Publication with column list (PG 15+): only listed columns appear in stream.
- `publish` operations respected: if `delete` not in publish list, deletes not delivered.

**Reconnection:**
- Simulate connection drop (restart PostgreSQL container): source reconnects, streaming resumes.
- Simulate network partition (pause container): source detects, enters RECONNECTING, resumes after unpause.
- Multiple reconnect attempts with backoff: verify timing and attempt count.

**Schema evolution (real DDL):**
- `ALTER TABLE ADD COLUMN`: target receives schema change, new rows include column.
- `ALTER TABLE DROP COLUMN`: target receives schema change.
- `ALTER TABLE ALTER COLUMN TYPE`: target receives schema change.

**Re-baseline:**
- Trigger `ReloadTable`: stream paused, target cleared, baseline re-run, stream resumed with catch-up.
- Changes during re-baseline: accumulated and applied after baseline complete.
- Idempotency window: overlapping rows from baseline and catch-up don't cause duplicates in final state.

**Edge cases:**
- Empty table baseline: no rows, target is READY with count 0.
- Table with no primary key: behavior defined (error or fallback).
- Very long-running streaming (10 minutes of continuous changes): no memory leaks, stable performance.
- Concurrent DDL during streaming: handled gracefully.

```go
func TestPostgresBaseline(t *testing.T) {
    pg := pgtest.NewSharedPostgres(t)
    pg.CreateTestTable(t, "public", "config_document", `
        CREATE TABLE public.config_document (
            id BIGSERIAL PRIMARY KEY,
            instance_id TEXT NOT NULL,
            key TEXT NOT NULL,
            data JSONB,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        )
    `)

    // Seed 100 rows
    for i := 0; i < 100; i++ {
        pg.InsertRow(t, "public", "config_document", map[string]any{
            "instance_id": fmt.Sprintf("inst_%03d", i),
            "key":         fmt.Sprintf("key_%03d", i),
            "data":        fmt.Sprintf(`{"index": %d}`, i),
        })
    }

    // Build engine with real PG source
    target := memory.NewIndexedTarget(
        memory.LookupFields("instance_id", "key"),
    )

    engine, errs := tablesync.NewEngine(
        tablesync.WithSource("pg", pgsource.New(
            pgsource.Connection(pg.ConnStr()),
            pgsource.SlotMode(pgsource.Ephemeral),
        )),
        tablesync.WithPipeline("pg", tablesync.Table("public", "config_document"), target),
    )
    require.Empty(t, errs)

    engine.Start()
    defer engine.Stop()

    require.True(t, engine.AwaitReady(30*time.Second))
    assert.Equal(t, int64(100), target.Count())

    // Verify specific row
    row, ok := target.Lookup("inst_042", "key_042")
    assert.True(t, ok)
    assert.Equal(t, `{"index": 42}`, row.GetString("data"))
}

func TestPostgresStreamingInsertUpdateDelete(t *testing.T) {
    pg := pgtest.NewSharedPostgres(t)
    pg.CreateTestTable(t, "public", "items", `
        CREATE TABLE public.items (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL,
            value INTEGER
        )
    `)

    target := memory.NewIndexedTarget(memory.LookupFields("name"))

    engine, _ := tablesync.NewEngine(
        tablesync.WithSource("pg", pgsource.New(
            pgsource.Connection(pg.ConnStr()),
            pgsource.SlotMode(pgsource.Ephemeral),
        )),
        tablesync.WithPipeline("pg", tablesync.Table("public", "items"), target),
    )
    engine.Start()
    defer engine.Stop()
    engine.AwaitReady(30 * time.Second)

    // INSERT
    pg.InsertRow(t, "public", "items", map[string]any{"name": "alpha", "value": 10})
    assert.Eventually(t, func() bool {
        _, ok := target.Lookup("alpha")
        return ok
    }, 5*time.Second, 50*time.Millisecond)

    row, _ := target.Lookup("alpha")
    assert.Equal(t, 10, row.GetInt("value"))

    // UPDATE
    pg.Exec(t, "UPDATE public.items SET value = 20 WHERE name = 'alpha'")
    assert.Eventually(t, func() bool {
        row, _ := target.Lookup("alpha")
        return row.GetInt("value") == 20
    }, 5*time.Second, 50*time.Millisecond)

    // DELETE
    pg.Exec(t, "DELETE FROM public.items WHERE name = 'alpha'")
    assert.Eventually(t, func() bool {
        _, ok := target.Lookup("alpha")
        return !ok
    }, 5*time.Second, 50*time.Millisecond)

    assert.Equal(t, int64(0), target.Count())
}

func TestPostgresStatefulResume(t *testing.T) {
    pg := pgtest.NewSharedPostgres(t)
    pg.CreateTestTable(t, "public", "docs", `
        CREATE TABLE public.docs (
            id BIGSERIAL PRIMARY KEY,
            content TEXT NOT NULL
        )
    `)
    pg.InsertRow(t, "public", "docs", map[string]any{"content": "initial"})

    slotName := fmt.Sprintf("test_slot_%d", time.Now().UnixNano())

    // First run: baseline + stream some changes
    target1 := memory.NewIndexedTarget(memory.LookupFields("content"))
    engine1, _ := tablesync.NewEngine(
        tablesync.WithSource("pg", pgsource.New(
            pgsource.Connection(pg.ConnStr()),
            pgsource.SlotMode(pgsource.Stateful),
            pgsource.SlotName(slotName),
        )),
        tablesync.WithPipeline("pg", tablesync.Table("public", "docs"), target1),
    )
    engine1.Start()
    engine1.AwaitReady(30 * time.Second)

    pg.InsertRow(t, "public", "docs", map[string]any{"content": "second"})
    waitForCount(t, target1, 2)

    engine1.Stop()  // clean shutdown, ACKs position

    // Insert a row while the engine is down
    pg.InsertRow(t, "public", "docs", map[string]any{"content": "while_down"})

    // Second run: should resume, not re-baseline
    target2 := memory.NewIndexedTarget(memory.LookupFields("content"))
    engine2, _ := tablesync.NewEngine(
        tablesync.WithSource("pg", pgsource.New(
            pgsource.Connection(pg.ConnStr()),
            pgsource.SlotMode(pgsource.Stateful),
            pgsource.SlotName(slotName),
        )),
        tablesync.WithPipeline("pg", tablesync.Table("public", "docs"), target2),
    )
    engine2.Start()
    defer engine2.Stop()

    // Should receive the row inserted while down via WAL replay
    assert.Eventually(t, func() bool {
        _, ok := target2.Lookup("while_down")
        return ok
    }, 10*time.Second, 100*time.Millisecond)
}

func TestPostgresReconnectAfterContainerRestart(t *testing.T) {
    pg := pgtest.NewSharedPostgres(t)
    pg.CreateTestTable(t, "public", "items", `
        CREATE TABLE public.items (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL
        )
    `)

    target := memory.NewIndexedTarget(memory.LookupFields("name"))
    observer := testobserver.New()

    engine, _ := tablesync.NewEngine(
        tablesync.WithSource("pg", pgsource.New(
            pgsource.Connection(pg.ConnStr()),
            pgsource.SlotMode(pgsource.Stateful),
            pgsource.SlotName("reconnect_test"),
            pgsource.Reconnect(5, 1*time.Second, 10*time.Second, 2.0),
        )),
        tablesync.WithPipeline("pg", tablesync.Table("public", "items"), target),
        tablesync.WithObserver(observer),
    )
    engine.Start()
    defer engine.Stop()
    engine.AwaitReady(30 * time.Second)

    pg.InsertRow(t, "public", "items", map[string]any{"name": "before_restart"})
    waitForCount(t, target, 1)

    // Restart PostgreSQL container
    pg.Restart(t)

    // Source should reconnect automatically
    assert.Eventually(t, func() bool {
        return observer.HasEvent("SourceConnected")
    }, 30*time.Second, 500*time.Millisecond)

    // Insert after restart — should be delivered
    pg.InsertRow(t, "public", "items", map[string]any{"name": "after_restart"})
    assert.Eventually(t, func() bool {
        _, ok := target.Lookup("after_restart")
        return ok
    }, 10*time.Second, 100*time.Millisecond)
}

func TestPostgresPublicationAutoCreate(t *testing.T) {
    pg := pgtest.NewSharedPostgres(t)
    pg.CreateTestTable(t, "public", "docs", `
        CREATE TABLE public.docs (
            id BIGSERIAL PRIMARY KEY,
            status TEXT NOT NULL,
            content TEXT
        )
    `)

    pubName := fmt.Sprintf("test_pub_%d", time.Now().UnixNano())
    slotName := fmt.Sprintf("test_slot_%d", time.Now().UnixNano())

    target := memory.NewIndexedTarget(memory.LookupFields("content"))
    engine, _ := tablesync.NewEngine(
        tablesync.WithSource("pg", pgsource.New(
            pgsource.Connection(pg.ConnStr()),
            pgsource.SlotMode(pgsource.Ephemeral),
            pgsource.SlotName(slotName),
            pgsource.Publication(
                pgsource.PubName(pubName),
                pgsource.PubCreate(true),
                pgsource.PubPublish("insert", "update", "delete"),
            ),
        )),
        tablesync.WithPipeline("pg", tablesync.Table("public", "docs"), target),
    )
    engine.Start()
    defer engine.Stop()
    engine.AwaitReady(30 * time.Second)

    // Verify publication was created
    var exists bool
    pg.QueryRow(t, "SELECT EXISTS(SELECT 1 FROM pg_publication WHERE pubname = $1)", pubName).Scan(&exists)
    assert.True(t, exists)

    // Verify table is in publication
    var tableCount int
    pg.QueryRow(t, "SELECT count(*) FROM pg_publication_tables WHERE pubname = $1", pubName).Scan(&tableCount)
    assert.Equal(t, 1, tableCount)
}
```

#### 22.4.3 Test Helpers

Standard helpers to reduce boilerplate in testcontainer tests:

```go
// waitForCount waits for a target to reach a specific row count.
func waitForCount(t *testing.T, target *memory.IndexedTarget, expected int64) {
    t.Helper()
    assert.Eventually(t, func() bool {
        return target.Count() == expected
    }, 10*time.Second, 50*time.Millisecond,
        "expected %d rows, got %d", expected, target.Count())
}

// waitForRow waits for a specific row to appear in the target.
func waitForRow(t *testing.T, target *memory.IndexedTarget, keyValues ...any) tablesync.Row {
    t.Helper()
    var row tablesync.Row
    assert.Eventually(t, func() bool {
        r, ok := target.Lookup(keyValues...)
        if ok {
            row = r
        }
        return ok
    }, 10*time.Second, 50*time.Millisecond,
        "row with key %v not found", keyValues)
    return row
}

// testobserver.New() returns an observer that records all events for assertion.
type TestObserver struct {
    mu     sync.Mutex
    events []ObservedEvent
}

func (o *TestObserver) HasEvent(eventType string) bool { /* ... */ }
func (o *TestObserver) EventCount(eventType string) int { /* ... */ }
func (o *TestObserver) WaitForEvent(eventType string, timeout time.Duration) bool { /* ... */ }
func (o *TestObserver) LastEventOf(eventType string) ObservedEvent { /* ... */ }
```

#### 22.4.4 PostgreSQL Version Matrix

Testcontainer tests run against multiple PostgreSQL versions to ensure compatibility:

| Version | Coverage |
|---|---|
| PostgreSQL 14 | Baseline logical replication, no row filters. |
| PostgreSQL 15 | Row filters and column lists in publications. |
| PostgreSQL 16 | Latest stable, full feature set. |

In CI, the version is parameterized:

```go
func TestPostgresVersions(t *testing.T) {
    versions := []string{"14-alpine", "15-alpine", "16-alpine"}
    for _, version := range versions {
        t.Run(version, func(t *testing.T) {
            pg := pgtest.NewPostgres(t, pgtest.WithImage("postgres:"+version))
            // ... run test suite ...
        })
    }
}
```

### 22.5 Tier 4: End-to-End Tests

Full system tests that start the pre-built service in Docker (or via `go run`), connect via the gRPC API (using the `tsync` CLI or a gRPC test client), and verify end-to-end behavior including health checks, metrics, and graceful shutdown.

#### 22.5.1 Setup

```go
func TestE2E(t *testing.T) {
    // Start PostgreSQL
    pg := pgtest.NewSharedPostgres(t)
    pg.CreateTestTable(t, "public", "config_document", ddl)
    pg.SeedRows(t, "public", "config_document", seedData)

    // Write config file
    configPath := writeTestConfig(t, pg.ConnStr())

    // Start tablesync-server as a subprocess
    server := startServer(t, configPath)
    defer server.Stop()

    // Wait for readiness
    waitForHealthy(t, "http://localhost:8080/health/ready", 60*time.Second)

    // Connect gRPC client
    conn, err := grpc.Dial("localhost:4001", grpc.WithInsecure())
    require.NoError(t, err)
    defer conn.Close()

    oamClient := pb.NewTableSyncOAMClient(conn)
    queryClient := pb.NewTableSyncQueryClient(conn)

    // ... run tests against the live service ...
}
```

#### 22.5.2 E2E Test Coverage

- **Health endpoints**: `/health/live` returns 200 immediately, `/health/ready` returns 200 after baseline.
- **gRPC OAM**: `GetStatus` returns correct service state, source info, pipeline states.
- **gRPC Query**: `Lookup`, `ListRows`, `CountRows` return correct data after baseline.
- **gRPC Subscribe**: Server-streaming subscription receives live changes.
- **CLI**: `tsync status`, `tsync query`, `tsync watch` produce correct output (test via subprocess).
- **Reload**: Trigger `ReloadTable` via gRPC, verify re-baseline occurs.
- **Pause/Resume**: Pause via gRPC, insert rows, resume, verify rows delivered.
- **Snapshot lifecycle**: Create snapshot via gRPC, list snapshots, restore from snapshot.
- **Graceful shutdown**: Send SIGTERM, verify shutdown snapshot taken, process exits 0.
- **Metrics**: Prometheus `/metrics` endpoint exposes expected counters and gauges.
- **Config validation**: Start with invalid config, verify process exits non-zero with clear error.
- **Fan-out**: Start fan-out target, connect Go client, verify snapshot + live stream delivery.

### 22.6 Test Observer

A recording observer implementation used across all test tiers:

```go
type TestObserver struct {
    mu     sync.Mutex
    events []ObservedEvent
    waitCh map[string][]chan struct{}
}

type ObservedEvent struct {
    Type      string
    Timestamp time.Time
    Args      map[string]any
}

// Implements all EngineObserver methods, records every call.
func (o *TestObserver) OnSourceConnected(sourceID, sourceType string) {
    o.record("SourceConnected", map[string]any{"sourceID": sourceID, "sourceType": sourceType})
}
func (o *TestObserver) OnChangeApplied(pipelineID string, table tablesync.TableID, action tablesync.ChangeAction, duration time.Duration) {
    o.record("ChangeApplied", map[string]any{"pipelineID": pipelineID, "table": table, "action": action, "duration": duration})
}
// ... all other observer methods ...

// Assertion helpers
func (o *TestObserver) HasEvent(eventType string) bool
func (o *TestObserver) EventCount(eventType string) int
func (o *TestObserver) Events(eventType string) []ObservedEvent
func (o *TestObserver) WaitForEvent(eventType string, timeout time.Duration) bool
func (o *TestObserver) WaitForEventCount(eventType string, count int, timeout time.Duration) bool
func (o *TestObserver) Reset()
```

### 22.7 Benchmark Tests

Performance-focused tests that measure throughput and latency under load:

```go
func BenchmarkBaselineLoad(b *testing.B) {
    // Measure rows/second for baseline loading into indexed-memory.
    // Test with 1K, 10K, 100K, 1M rows.
}

func BenchmarkStreamingThroughput(b *testing.B) {
    // Measure changes/second for streaming into indexed-memory.
    // Test with various change rates.
}

func BenchmarkIndexLookup(b *testing.B) {
    // Measure lookup latency on indexed-memory target with various index types.
    // Test with 1K, 10K, 100K rows.
}

func BenchmarkSnapshotCreate(b *testing.B) {
    // Measure time and size for snapshotting N rows.
}

func BenchmarkSnapshotRestore(b *testing.B) {
    // Measure time for restoring from snapshot vs full baseline.
}

func BenchmarkFanOutBroadcast(b *testing.B) {
    // Measure broadcast latency to N connected fan-out clients.
}
```

### 22.8 CI Configuration

```yaml
# .github/workflows/test.yml
name: Test

on: [push, pull_request]

jobs:
  unit:
    name: "Tier 1: Unit Tests"
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test -short -race -count=1 ./...

  integration:
    name: "Tier 2: Integration Tests"
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test -run 'TestIntegration' -race -count=1 ./...

  testcontainers:
    name: "Tier 3: Testcontainers (PG ${{ matrix.pg-version }})"
    runs-on: ubuntu-latest
    strategy:
      matrix:
        pg-version: ['14-alpine', '15-alpine', '16-alpine']
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test -run 'TestPostgres' -race -count=1 -timeout 10m ./...
        env:
          POSTGRES_TEST_IMAGE: postgres:${{ matrix.pg-version }}

  e2e:
    name: "Tier 4: End-to-End"
    runs-on: ubuntu-latest
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test -run 'TestE2E' -race -count=1 -timeout 15m ./...

  benchmarks:
    name: "Benchmarks"
    runs-on: ubuntu-latest
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test -run '^$' -bench '.' -benchmem -count=3 ./...
```

---

## 23. Configuration Validation

The engine validates the full configuration before starting any source or pipeline. Validation runs in the `Build()` phase (library API) or during config loading (pre-built service). All errors are collected and reported together rather than failing on the first error.

### 23.1 Validation Checks

**Structural validation** (before connecting to anything):

- All referenced source IDs in table configs exist in the sources map.
- No duplicate pipeline IDs (source + table + target type must be unique, or explicitly named).
- Index fields in target configs are non-empty.
- Buffer sizes, retry counts, intervals are within valid ranges.
- Port numbers don't collide (gRPC OAM, fan-out gRPC, health HTTP, metrics).
- Snapshot store config is present if snapshots are enabled.
- TTL config specifies a valid field name or a computed function.
- HOCON syntax is valid and all required fields are present.

**Source validation** (connects to sources, calls `validate_tables()`):

- Source is reachable.
- Configured tables exist.
- Connection user has required permissions (SELECT, replication, publication management if applicable).
- For PostgreSQL: `wal_level = logical`, sufficient `max_replication_slots`, publication exists (if `create = false`).
- For Kinesis: stream exists, shards are accessible, checkpoint table is writable.

**Cross-reference validation**:

- Filter/transform field references match the discovered column schema.
- Index fields in target configs exist in the table's column schema.
- Primary key config matches the actual table primary key.

### 23.2 API

```go
// Library API — validation happens at Build()
engine, errors := tablesync.NewEngine().
    Source("pg_main", pgSource).
    Pipeline("pg_main", Table("public", "config_document"), target).
    Build()

if len(errors) > 0 {
    for _, err := range errors {
        log.Errorf("config error: %s", err)
    }
    os.Exit(1)
}

// Pre-built service — also available as a CLI command
// $ tablesync-server validate --config /etc/tablesync/tablesync.conf
```

### 23.3 Validation Modes

- **Strict** (default for pre-built service): all validations run including source connectivity. Fails startup if any error.
- **Structural only** (flag: `--validate=structural`): only config structure, no network calls. Useful in CI/CD pipelines.
- **Skip** (flag: `--validate=none`): skip validation entirely. For emergency deployments.

---

## 24. Shutdown Ordering

Graceful shutdown follows a precise sequence to ensure no data loss and clean disconnection of all components.

```
on SIGTERM / engine.Stop():

    1. Stop accepting new fan-out client connections.
       (Fan-out gRPC server stops accepting new Sync streams.)

    2. Notify connected fan-out clients of impending shutdown.
       (Send a shutdown notification message on all active streams.)

    3. Drain fan-out client streams.
       (Wait up to drain_timeout for clients to disconnect gracefully.
        Force-close any remaining streams after timeout.)

    4. Pause all sources.
       (Sources stop delivering changes. Connections stay open to
        retain position/slots. Pipelines drain their buffers.)

    5. Flush all pipeline buffers to targets.
       (HTTP target flushes pending batches. In-memory targets
        are already up to date.)

    6. Take shutdown snapshot (if snapshot.on_shutdown = true).
       (All sources paused, all buffers flushed — consistent point.)

    7. ACK final positions to all sources.
       (Advance each source's ACK to the last fully processed position.)

    8. Close all targets.
       (Targets run on_close — clear state, deregister listeners.)

    9. Close all sources.
       (PostgreSQL: close replication connection, release slot.
        Kinesis: checkpoint final position, close shard consumers.)

   10. Stop gRPC servers (OAM, Query, Fan-out).

   11. Stop health HTTP server.

   12. Stop metrics server.

   13. Exit 0.
```

**Timeout**: The entire shutdown sequence has a configurable `shutdown_timeout` (default: 120s). If the sequence hasn't completed by then, the process logs a warning and exits non-zero.

```hocon
shutdown {
  timeout = 120s
  drain_timeout = 10s       # max time to wait for fan-out clients to disconnect
}
```

**Crash vs. graceful**: On ungraceful termination (SIGKILL, OOM, panic), nothing is guaranteed. On restart:

- Stateful sources resume from last ACKed position — changes since the last ACK are redelivered.
- Ephemeral sources do a full re-baseline.
- If a shutdown snapshot was not taken, the next startup either restores the last available snapshot or does a full baseline.

---

## 25. Schema Evolution — v1 Scope

v1 handles the common, simple schema evolution cases. Complex cases are treated as errors requiring operator intervention.

### 25.1 Handled in v1

| Change | Detection | Behavior |
|---|---|---|
| **Column added** | New column appears in relation message / stream schema. | `on_schema_change` called on all targets. In-memory targets: add column to schema, new rows include it, existing rows have `null` for the new column. HTTP target: forwards new shape. |
| **Column removed** | Column disappears from relation message. | `on_schema_change` called. In-memory targets: if column is not part of any index, remove from schema. If column is part of an index, target returns `RE_BASELINE`. HTTP target: forwards new shape. |
| **Column renamed** | Detected as remove + add (PostgreSQL logical replication does not distinguish rename from drop+add). | Treated as column removed + column added. If the removed column was indexed, triggers `RE_BASELINE`. |
| **Column type changed** | Column same name, different type in relation message. | `on_schema_change` called. In-memory targets: if column is part of an index, return `RE_BASELINE`. Otherwise `CONTINUE` and let values coerce naturally. HTTP target: `CONTINUE`. |

### 25.2 Not handled in v1 (returns ERROR)

- Table renamed.
- Primary key changed (columns added/removed from PK).
- Table dropped from the source while streaming.
- Multiple columns changed simultaneously in a way that makes the schema unrecognizable.

These will cause the affected pipeline to enter `ERROR` state. The operator must manually reconfigure and reload.

---

## 26. v1 Scope and Boundaries

This section defines what is in scope for the first version and what is explicitly deferred.

### 26.1 In v1

**Core engine (Go)**:
- Pipeline orchestration: multiple sources, multiple tables, multiple targets per table.
- Push-based change delivery to all targets with flow control.
- ACK coordination (minimum confirmed position across all pipelines per source).
- Pipeline-level filters and transforms (built-in types + custom via Go functions).
- Pipeline-level error isolation with configurable policies (isolate / stop_source / stop_all).
- Per-pipeline bounded change buffers with backpressure policies.
- Pipeline state machine independent of global service state.
- Readiness signaling (per-pipeline, per-source, global).
- Configuration validation at startup (structural + source connectivity).
- EngineObserver interface for all observability.
- Explicit shutdown ordering.

**Sources**:
- PostgreSQL logical replication (`pgoutput`) with ephemeral and stateful modes.
- PostgreSQL publication auto-create/manage with per-table row filters and column lists (PG 15+).
- PostgreSQL connection management with reconnect strategy.
- S3 + Kinesis source with checkpointing.
- Test source for integration testing.

**Targets**:
- HTTP sync with internal batching (`batch_size` + `flush_interval`) and proper `is_durable` tracking for unflushed buffers.
- Compiled in-memory with pluggable compiler, filtering, and keyed lookup.
- Indexed in-memory with configurable secondary indexes (unique + non-unique).
- Replication fan-out (separate module) with journal, periodic snapshots, gRPC replication service, client backpressure management.

**Snapshots**:
- Engine-level snapshots with pause-based isolation.
- Pluggable snapshot store (local disk, S3).
- Pluggable snapshot serializer (JSONL default).
- User metadata on snapshots for cross-instance compatibility checks.
- Scheduled, manual, and on-shutdown snapshot triggers.
- Snapshot restore on startup with position verification.

**Schema evolution**:
- Column added, removed, renamed (detected as remove+add), type changed.
- Target-specific response (CONTINUE, RE_BASELINE, ERROR).
- Everything else → ERROR.

**TTL / expiry**:
- Field-based and computed expiry for in-memory targets.
- Periodic scan with configurable interval.
- Proper handling of updates that extend/don't extend expiry.

**Validation / consistency checks**:
- Post-baseline row count verification.
- Periodic row count drift detection.

**Dead letter**:
- Capture failed changes to a dead letter store.
- Dead letter replay and purge deferred to v1.1 (capture only in v1).

**gRPC services**:
- OAM service (status, source info, reload, pause/resume, reset, snapshot management, dead letter listing).
- Query service (lookup, list, count, subscribe).
- Replication service (sync, list snapshots, fetch snapshot, status).

**CLI (`tsync`)**:
- All commands documented in this spec.

**Fan-out client libraries**:
- Go client library with local snapshot caching, auto-reconnect, and in-memory state building.
- Java client library.
- TypeScript client library.

**Pre-built service**:
- HOCON config loading with env var overrides.
- Docker image with init hooks, health checks, graceful shutdown.
- Docker Compose example.
- Kubernetes deployment manifest.

### 26.2 Explicitly deferred (not in v1)

- **Pull-mode / ChangeLoader** — all targets use push with internal batching. Reconsider if push proves insufficient.
- **Publication filter push-down from pipeline filters** — publication config is explicit on the source; pipeline filters are independent. Automatic push-down is a future optimization.
- **`supports_consistent_snapshot()` optimization** — engine always pauses during snapshot. Future: allow RocksDB or COW targets to snapshot without pause.
- **Dead letter replay via gRPC/CLI** — v1 captures dead letters. Replay/purge via API comes in v1.1.
- **SQL + Kafka source** — deferred. Start with PostgreSQL and S3+Kinesis.
- **Java core library** — Go is the primary language. Java port comes later.
- **RocksDB target** — future.
- **Dry run / preview mode**.
- **Multi-table transaction boundaries** — grouped commit delivery.
- **Copy-on-write / lock-free snapshot isolation** for in-memory targets.
- **Target-native snapshot delegation** (e.g., RocksDB SST files).
- **Computed / materialized views** across synced tables.
- **Conditional ACK strategies** (batch ACK, time-windowed ACK).
- **Multiple fan-out tables on the same gRPC port** — v1: one port per fan-out target.

### 26.3 Implementation Order

Testing is built alongside every component — never deferred. Each phase includes the tests for that phase's functionality.

```
Phase 1 — Core engine + PostgreSQL + in-memory targets
    1. Core types: Position, TableIdentifier, ColumnDefinition, ChangeEvent, Row
       + unit tests for all types
    2. SyncSource interface + test source implementation
       + unit tests for test source
    3. SyncTarget interface
    4. Indexed in-memory target (most testable, exercises all target methods)
       + unit tests for index structures, BiMap, composite keys
    5. Compiled in-memory target
       + unit tests for compiler dispatch, key extraction, filtering
    6. Engine: pipeline orchestration, push delivery, ACK coordination
       + Tier 2 integration tests: baseline, streaming, ACK, fan-out ACK
    7. Pipeline filters and transforms
       + unit tests for each built-in filter/transform
       + Tier 2: filters and transforms in pipeline
    8. Backpressure / buffering
       + unit tests for buffer policies
       + Tier 2: backpressure scenarios
    9. Pipeline error isolation
       + Tier 2: error policy tests (isolate, stop_source, stop_all)
   10. PostgreSQL source (ephemeral mode first, then stateful)
       + Tier 3 testcontainers: baseline, streaming, WAL ACK, slot management
   11. PostgreSQL publication management
       + Tier 3: auto-create, alter, row filters, column lists, PG version matrix
   12. Connection management / reconnect
       + Tier 3: container restart, pause/unpause, max retries

Phase 2 — Snapshots + schema evolution + TTL
   13. Snapshot store interface + JSONL serializer + local disk store
       + unit tests for JSONL round-trip, store operations
   14. Engine snapshot orchestration (pause, export, resume)
       + Tier 2: snapshot create, list, metadata
   15. Snapshot restore on startup
       + Tier 2: restore then resume streaming
       + Tier 3: snapshot restore with real PG source
   16. S3 snapshot store
       + unit tests with mock S3 or LocalStack
   17. Schema evolution detection + target responses
       + Tier 2: add/remove/rename/type change with test source
       + Tier 3: real ALTER TABLE against PG
   18. TTL / expiry
       + unit tests for expiry calculation
       + Tier 2: TTL lifecycle (insert, expire, update-extend, update-still-expired)
   19. Validation / row count checks
       + Tier 2: count match/mismatch scenarios
       + Tier 3: row count against real PG
   20. Dead letter capture
       + Tier 2: failed changes captured to dead letter store

Phase 3 — gRPC + CLI + observability
   21. EngineObserver + test observer
       + Tier 2: verify all observer callbacks fire correctly
   22. gRPC OAM service
       + Tier 2: OAM RPCs against engine with test source
   23. gRPC Query service
       + Tier 2: Query RPCs against in-memory targets
   24. Prometheus/OTel metrics bridges
       + unit tests: observer events map to correct metrics
   25. Readiness signaling + health endpoints
       + Tier 2: readiness lifecycle
   26. Configuration validation
       + unit tests: structural validation catches errors
       + Tier 3: source validation against real PG
   27. HOCON config loader
       + unit tests: parse, env var override, conf.d merge
   28. CLI tool (tsync)
       + Tier 4: CLI commands against running service
   29. Shutdown ordering
       + Tier 2: verify shutdown sequence with test source
       + Tier 3: verify with real PG (slot released, position ACKed)

Phase 4 — Fan-out + additional sources
   30. Replication fan-out module (target + journal + snapshots + gRPC)
       + unit tests for journal, snapshot scheduling
       + Tier 2: fan-out target with test source, client connect/sync/stream
   31. Fan-out Go client library
       + Tier 2: client lifecycle, reconnect, local snapshot
   32. Fan-out Java client library
       + integration tests in Java test suite
   33. Fan-out TypeScript client library
       + integration tests in TS test suite
   34. S3 + Kinesis source
       + unit tests with mock/LocalStack
   35. HTTP sync target
       + unit tests with httptest server
       + Tier 2: batching, flush interval, is_durable tracking

Phase 5 — End-to-end + packaging
   36. Pre-built service binary
   37. Docker image + Dockerfile + health checks
   38. Tier 4 end-to-end tests: full service in Docker
   39. Benchmark tests
   40. Documentation
```

---

## 27. Future Work

Items noted but not designed in this specification:

- **Pull-mode / ChangeLoader** — alternative to push for targets that want to control their own batching.
- **Publication filter push-down** — automatically derive publication WHERE clauses from pipeline filters.
- **Dry run / preview mode** — start engine, run baseline, report what would be synced without persisting or streaming.
- **Multi-table transaction boundaries** — grouped commit delivery across tables within a single transaction.
- **Copy-on-write / lock-free snapshot isolation** — for in-memory targets, allowing snapshots without pausing the stream.
- **Target-native snapshot delegation** — e.g., RocksDB SST file snapshots managed entirely by the target.
- **Computed / materialized views** — derived tables computed from synced table data.
- **Conditional ACK strategies** — ACK after N changes or after a time window rather than every change.
- **RocksDB target implementation** — persistent on-disk target with native snapshot support.
- **SQL + Kafka source** — generic SQL baseline with Kafka CDC stream.
- **Java core library port** — full port of tablesync-core to Java/Kotlin.
- **Fan-out gRPC port multiplexing** — multiple fan-out tables served on a single gRPC port.
- **Dead letter replay/purge via gRPC and CLI** — operational tooling for dead letter management (capture ships in v1).
