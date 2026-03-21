# Laredo — Road to v1.0

Everything needed to go from scaffolding to a production-ready, stable release with full documentation.

---

## 1. Core Library (`laredo` root package)

### 1.1 Types & Interfaces

- [x] Finalize `Value` type — type alias to `any`
- [x] Add `Row.Get(key) (Value, bool)` accessor
- [x] Add `Row.Keys() iter.Seq[string]` for deterministic iteration
- [x] `Position` — keep opaque as type alias to `any`; sources handle serialization via `PositionToString`/`PositionFromString`
- [x] `ChangeEvent` — add `CommitTimestamp *time.Time` field for sources that provide commit-level timestamps
- [x] `ColumnDefinition` — add `OrdinalPosition`, `PrimaryKeyOrdinal`, `DefaultValue`, `TypeOID`, `MaxLength`
- [x] `IndexDefinition` — parity confirmed; spec's `IndexInfo.entry_count` is runtime state, not definition
- [x] `TableIdentifier` — implement `encoding.TextMarshaler`/`TextUnmarshaler` for config round-tripping
- [x] Add `CompositeObserver` that fans out to multiple `EngineObserver` implementations
- [x] Add no-op `NullObserver` for embedded users who don't need observability

### 1.2 Engine Interface

- [x] Implement `NewEngine()` — validate config, wire sources/pipelines/targets, return errors
- [x] `GetTarget[T]()` generic accessor — retrieve a typed target from the engine for direct querying
- [x] Pipeline ID generation: `"{sourceID}:{schema}.{table}:{targetType}"`
- [x] `Engine.Targets()` — lookup targets by source and table (supports `GetTarget[T]`)
- [x] Lifecycle stubs: `Start`/`Stop` with state tracking (started/stopped guards)
- [x] `Engine.Start()` — launch goroutines for each source, begin baseline or restore
- [x] `Engine.Stop()` — graceful shutdown: drain buffers, close sources/targets (snapshot-on-shutdown deferred to §5.4)
- [x] `Engine.AwaitReady()` — block until all pipelines reach `STREAMING` or timeout
- [x] `Engine.IsReady()` — global readiness (per-source/per-table/per-pipeline variants deferred)
- [x] `Engine.OnReady(callback)` — callback-style readiness notification
- [x] `Engine.Reload()` — trigger re-baseline for a specific table (spec §4.1.2 forced re-baseline)
- [x] `Engine.Pause()` / `Engine.Resume()` — per-source pause/resume
- [x] `Engine.CreateSnapshot()` — on-demand snapshot with user metadata

### 1.3 Replay Engine (spec §15)

- [x] `SnapshotReplay` builder API — `NewSnapshotReplay(store).Target(table, target).Run(ctx, id)`
- [x] `ReplaySpeed` — `ReplayFullSpeed`, `ReplayRealTime` constants defined
- [x] `replay.Run()` blocking and `replay.Start()` async variants
- [x] Wire replay into OAM gRPC service (`StartReplay`, `GetReplayStatus`, `StopReplay`)

---

## 2. Engine Implementation (`internal/engine/`)

### 2.1 Pipeline Orchestrator

- [x] Source registration and lifecycle management (init, connect, close)
- [x] Pipeline construction: bind (source, table, filters, transforms, target)
- [x] Pipeline ID generation: `"{sourceID}:{schema}.{table}:{targetType}"`
- [x] Pipeline state machine: `INITIALIZING → BASELINING → STREAMING → PAUSED/ERROR/STOPPED`
- [x] Source demux: dispatch changes from one source stream to correct per-table pipelines
- [x] Multi-target fan-out: deliver each change to all targets for a table (same table, different target types)

### 2.2 Baseline & Startup Paths

- [x] Cold start — no snapshot, no resume: full baseline from source
- [x] Resume — source `SupportsResume()` + has last ACKed position: skip baseline, resume stream
- [x] Snapshot restore — load latest snapshot, restore targets via `RestoreSnapshot()`, resume from snapshot's source position
- [x] Snapshot unusable fallback — no source position or restore error → fall through to full baseline
- [x] Re-baseline flow (spec §4.1.2) — pause stream, `OnTruncate`, re-baseline rows, resume stream

### 2.3 Change Buffer

- [x] Bounded channel between source dispatcher and target (`ChangeBuffer[T]` generic)
- [x] Configurable size per pipeline
- [x] `Block` policy — backpressure to source (default Go channel blocking)
- [x] `DropOldest` policy — `SendDropOldest()` drops oldest item when full
- [x] `Error` policy — `TrySend()` returns false when full (caller marks ERROR)
- [x] Observer notifications on depth changes and policy triggers
- [x] Wire ChangeBuffer into engine dispatch path (per-pipeline consumer goroutines)

### 2.4 ACK Coordination

- [x] Track per-pipeline confirmed position (last change where `IsDurable() == true`)
- [x] Compute minimum confirmed position across all pipelines sharing a source
- [x] ACK the source at the minimum confirmed position
- [x] Handle pipelines in ERROR state (advance past them per `Isolate` policy)

### 2.5 Error Handling & Retry

- [x] Per-change retry with exponential backoff (configurable max_retries, 100ms initial, 5s max backoff)
- [x] `Isolate` policy — mark pipeline ERROR, continue others
- [x] `StopSource` policy — stop all pipelines on the source
- [x] `StopAll` policy — halt entire engine (cancels engine context)
- [x] Dead letter integration — write failed changes to dead letter store if enabled (`WithDeadLetterStore`)

### 2.6 Filter & Transform Chain

- [x] Apply `PipelineFilter` chain before target dispatch (baseline rows + change events)
- [x] Apply `PipelineTransform` chain (mutate row, nil = drop)
- [x] Handle filter on `DELETE` events (apply filters to old values; transforms not applied to identity)

### 2.7 TTL / Expiry (spec §10)

- [x] Field-based mode: `WithTTLField(fieldName)` reads time.Time or RFC 3339 string
- [x] Computed mode: `WithTTL(func(Row) time.Time)` pipeline option
- [x] Periodic scanner: `WithTTLScanInterval(d)` runs scanner goroutine per pipeline
- [x] On scan: remove expired rows from target via `OnDelete`, fire `OnRowExpired` observer
- [x] Skip insertion of already-expired rows on `INSERT` and baseline
- [x] Treat update-to-expired as delete

### 2.8 Readiness Tracking

- [x] Per-pipeline readiness: `true` when baseline complete and streaming
- [x] Per-source readiness: `IsSourceReady(sourceID)` — all pipelines on source are ready
- [x] Global readiness: all pipelines are ready
- [x] `AwaitReady` with timeout (channel + timer)
- [x] Callback registration (`OnReady`)

### 2.9 Graceful Shutdown

- [x] Stop accepting new work
- [x] Flush all pipeline buffers
- [x] Take snapshot if `snapshotOnShutdown` is configured
- [x] Close all targets
- [x] ACK final positions
- [x] Close all sources
- [x] Configurable shutdown timeout (`WithShutdownTimeout`)

### 2.10 Validation (spec §12)

- [x] Post-baseline row count verification (dispatched count vs target.Count())
- [x] Periodic row count drift detection on configurable interval
- [x] `OnValidationResult` observer callback (fires after baseline with match/mismatch)
- [x] Configurable action on mismatch: `WithValidationAction` (Warn/ReBaseline/Fail)

---

## 3. Sources

### 3.1 PostgreSQL (`source/pg/`)

- [x] Connection management — query connection with schema discovery (replication connection deferred to streaming)
- [ ] `pgoutput` logical decoding (built-in plugin, no extensions needed)
- [ ] Ephemeral mode: temporary replication slot, full baseline every startup
- [ ] Stateful mode: persistent named slot, resume from last ACKed LSN
- [ ] Publication management:
  - [ ] `create = false` — use existing publication, fail if not found
  - [ ] `create = true` — auto-create publication from configured tables
  - [ ] Sync publication on startup (add/remove tables, update publish operations)
  - [ ] Row filters and column lists (PostgreSQL 15+)
  - [ ] Publication naming: default `{slot_name}_pub`, configurable override
- [x] Baseline: `REPEATABLE READ` snapshot, `SELECT *` per table, return LSN position
- [ ] Streaming: consume `pgoutput` messages, decode into `ChangeEvent`
- [x] Position type: LSN (uint64 wrapper with string formatting `0/XXXXXXXX`)
- [x] `ComparePositions` — LSN comparison
- [ ] ACK — `StandbyStatusUpdate` with confirmed flush LSN
- [ ] Reconnect state machine: `CONNECTING → CONNECTED → STREAMING → RECONNECTING → ERROR`
  - [ ] Exponential backoff with configurable max_attempts, initial/max backoff, multiplier
  - [ ] Stateful mode reconnect: resume from last ACKed LSN
  - [ ] Ephemeral mode reconnect: signal engine for full re-baseline
- [ ] Slot health monitoring: lag bytes behind WAL tip
- [ ] `ResetSource` — drop and recreate slot (+ publication if auto-managed)
- [ ] `Pause` / `Resume` — stop reading from stream, keep connection open
- [ ] Schema change detection from replication stream (relation messages)
- [x] Option builder: `Connection()`, `SlotMode()`, `SlotName()`, `Publication()`, `Reconnect()`

### 3.2 S3 + Kinesis (`source/kinesis/`)

- [ ] Baseline: read S3 objects (JSONL/Parquet), build rows
- [ ] Schema discovery from S3 manifest or schema registry
- [ ] Stream: Kinesis shard consumer (enhanced fan-out or polling)
- [ ] Composite position: S3 object version + per-shard sequence numbers
- [ ] `ComparePositions` — compare composite positions
- [ ] ACK: Kinesis checkpoint (DynamoDB checkpoint table)
- [ ] Multi-shard: concurrent shard consumers, demux by table
- [ ] Shard split/merge handling
- [ ] `SupportsResume()` — true if checkpointing enabled
- [ ] Option builder: `BaselineBucket()`, `StreamARN()`, `ConsumerGroup()`, `CheckpointTable()`

### 3.3 Test Source (`source/testsource/`)

- [x] In-memory table data store
- [x] Programmable baseline: `AddRow()`, `SetSchema()`
- [x] Programmable stream: `EmitInsert()`, `EmitUpdate()`, `EmitDelete()`, `EmitTruncate()`
- [x] Position tracking: simple monotonic sequence
- [x] Error injection: `SetInitError()`, `SetBaselineError()`
- [x] Configurable delays: `SetBaselineRowDelay()`, `SetStreamDelay()`

---

## 4. Targets

### 4.1 HTTP Sync (`target/httpsync/`)

- [x] HTTP client with configurable base URL, timeout, auth header, custom headers
- [x] Batched baseline: buffer `OnBaselineRow`, flush at `batch_size` as `POST {base_url}/baseline/batch`
- [x] `POST {base_url}/baseline/start` on init, `POST {base_url}/baseline/complete` on complete
- [x] Change batching: buffer inserts/updates/deletes, flush at `batch_size` or `flush_interval` (whichever first)
- [x] `POST {base_url}/changes` with batched change payload
- [x] Retry with exponential backoff on HTTP failure
- [x] `IsDurable()` — `true` only after batch flush with 2xx response
- [x] `OnSchemaChange` — return `CONTINUE`, forward new schema shape
- [x] `OnClose` — best-effort flush remaining buffer
- [x] Export/restore snapshot (stateless target — may return empty/error)
- [x] Option builder: `BaseURL()`, `BatchSize()`, `FlushInterval()`, `Timeout()`, `RetryCount()`, `AuthHeader()`, `Headers()`

### 4.2 Compiled In-Memory (`target/memory/` — `CompiledTarget`)

- [x] Pluggable compiler function: `func(Row) (any, error)`
- [x] Key extractor from configured `key_fields`
- [x] Optional filter predicate
- [x] Store: `map[string]compiledEntry` keyed by composite key (key fields joined with `\x00`)
- [x] `OnBaselineRow` / `OnInsert` — filter → compile → insert, notify listeners `(nil, compiled)`
- [x] `OnUpdate` — filter → compile → replace, notify listeners `(oldCompiled, newCompiled)`. If no longer passes filter, treat as delete
- [x] `OnDelete` — remove by key, notify listeners `(oldCompiled, nil)`
- [x] `OnTruncate` — clear map, notify
- [x] `IsDurable()` — always `true`
- [x] `OnSchemaChange` — `RE_BASELINE` by default
- [x] Query API: `Get(keyValues...) any`, `All() iter.Seq2`, `Count() int`, `Listen(func(old, new any)) func()`
- [x] Export/restore snapshot
- [x] Option builder: `Compiler()`, `KeyFields()`, `CompiledFilter()`

### 4.3 Indexed In-Memory (`target/memory/` — `IndexedTarget`)

- [x] Primary row store: `map[primaryKey]Row`
- [x] Primary lookup index: unique composite key from `lookup_fields`
- [x] Additional indexes: unique (BiMap) and non-unique (multimap)
- [x] `OnInit` — validate all index fields exist in column schema, allocate data structures
- [x] `OnBaselineRow` / `OnInsert` — insert into store + all indexes, notify listeners `(nil, row)`
- [x] `OnUpdate` — lookup old row, replace in store, update all indexes, notify `(oldRow, newRow)`
- [x] `OnDelete` — remove from store + all indexes, notify `(oldRow, nil)`
- [x] `OnTruncate` — clear store + all indexes, notify
- [x] `IsDurable()` — always `true`
- [x] `OnSchemaChange` — new column: `CONTINUE`; dropped indexed column: `RE_BASELINE`
- [x] Query API: `Lookup(keyValues...) (Row, bool)`, `LookupAll(indexName, keyValues...) []Row`, `Get(pk) (Row, bool)`, `All() iter.Seq2`, `Count() int`, `Listen(func(old, new Row)) func()`
- [x] Export/restore snapshot
- [x] Thread safety: `sync.RWMutex` for concurrent reads during streaming
- [x] Option builder: `LookupFields()`, `AddIndex()`

### 4.4 Replication Fan-Out (`target/fanout/`)

- [ ] In-memory state: `map[primaryKey]Row`
- [ ] Change journal: bounded circular buffer of `JournalEntry` with monotonic sequence numbers
- [ ] Journal pruning: by `max_entries` and `max_age`
- [ ] Periodic snapshot scheduler: serialize in-memory state at current sequence, tag with journal position
- [ ] Snapshot retention: `keep_count`, `max_age`
- [ ] Client registry: track connected clients, their sequence position, backpressure state
- [ ] `OnBaselineRow` — insert into state + append INSERT to journal
- [ ] `OnBaselineComplete` — mark READY, take initial snapshot, start accepting clients
- [ ] `OnInsert`/`OnUpdate`/`OnDelete`/`OnTruncate` — update state, append to journal, broadcast to clients
- [ ] `OnSchemaChange` — append SCHEMA_CHANGE to journal, broadcast, take new snapshot
- [ ] `OnClose` — final snapshot, disconnect all clients, shut down gRPC server
- [ ] `IsDurable()` — always `true`
- [ ] Export/restore snapshot
- [ ] Per-client backpressure: configurable `max_size`, `drop_disconnect` / `slow_down` policies
- [ ] Heartbeats: periodic heartbeat messages on idle connections (default 5s)

#### 4.4.1 gRPC Replication Server (embedded in fan-out target)

- [ ] `Sync` RPC — primary server-streaming replication call:
  - [ ] Handshake: determine sync mode (FULL_SNAPSHOT / DELTA / DELTA_FROM_SNAPSHOT)
  - [ ] Full snapshot mode: send SnapshotBegin → SnapshotRow* → SnapshotEnd → journal catch-up → live
  - [ ] Delta mode: send journal entries from client's sequence → live
  - [ ] Delta-from-snapshot mode: tell client to use local snapshot, send journal delta → live
  - [ ] Atomic handoff: pin journal during snapshot send, no gaps
- [ ] `ListSnapshots` RPC — list available snapshots for client bootstrapping
- [ ] `FetchSnapshot` RPC — streaming download of a specific snapshot
- [ ] `GetReplicationStatus` RPC — current sequence, journal bounds, client count, per-client state
- [ ] TLS configuration
- [ ] Max clients limit

---

## 5. Snapshot System

### 5.1 JSONL Serializer (`snapshot/jsonl/`)

- [x] `Write()` — one JSON object per line, one row per line
- [x] `Read()` — streaming line-by-line JSON decode (bufio scanner, up to 10MB per line)
- [x] Header line with `TableSnapshotInfo` metadata
- [x] Handle all `Value` types (nil, string, int, float, bool, time, bytes, JSON)

### 5.2 Local Disk Store (`snapshot/local/`)

- [x] Directory structure: `{base_path}/{snapshot_id}/{table_schema}.{table_name}.jsonl`
- [x] Metadata file: `{snapshot_id}/metadata.json`
- [x] `Save` — create temp directory, write metadata + per-table JSONL files, atomic rename
- [x] `Load` — read metadata, stream per-table JSONL files via serializer
- [x] `Describe` — read metadata only, calculate total size
- [x] `List` — scan directory, filter by table, sort by creation time (newest first)
- [x] `Delete` — remove snapshot directory
- [x] `Prune` — delete all but N most recent (optionally per-table)
- [x] Atomic writes (write to `.tmp-{id}` dir, rename to final)

### 5.3 S3 Store (`snapshot/s3/`)

- [ ] S3 key structure: `{prefix}/{snapshot_id}/{table}.jsonl`
- [ ] Metadata object: `{prefix}/{snapshot_id}/metadata.json`
- [ ] `Save` — multipart upload for large tables
- [ ] `Load` — streaming GetObject
- [ ] `List` — ListObjectsV2 with prefix filtering
- [ ] `Delete` — delete all objects under snapshot prefix
- [ ] `Prune`
- [ ] Configurable: bucket, prefix, region, credentials

### 5.4 Snapshot Scheduler (in engine)

- [x] Periodic snapshots on configurable interval (`WithSnapshotSchedule`)
- [x] On-demand via `Engine.CreateSnapshot()`
- [x] Snapshot-on-shutdown
- [x] Snapshot creation flow: pause sources → export targets → write to store → resume sources
- [x] Retention enforcement after each snapshot (`WithSnapshotRetention`)

---

## 6. Pipeline Components

### 6.1 Built-in Filters (`filter/`)

- [x] `FieldEquals` — match rows where field equals a value
- [x] `FieldPrefix` — match rows where string field starts with prefix
- [x] `FieldRegex` — match rows where field matches regex
- [x] Config-driven construction from HOCON `type = field-equals` etc.

### 6.2 Built-in Transforms (`transform/`)

- [x] `DropFields` — remove specified fields
- [x] `RenameFields` — rename fields
- [x] `AddTimestamp` — add a field with current timestamp
- [x] Config-driven construction from HOCON `type = drop-fields` etc.

### 6.3 Dead Letter Store (`deadletter/`)

- [x] In-memory dead letter store (for testing) — `deadletter.NewMemoryStore()`
- [ ] S3 dead letter store
- [x] Local disk dead letter store — `deadletter.NewLocalStore(basePath)`
- [x] `Write` — append JSONL entries to `{pipelineID}.jsonl`
- [x] `Read` — read entries with optional limit
- [x] `Replay` — re-deliver dead letters to a target
- [x] `Purge` — remove pipeline's JSONL file

---

## 7. Config (`config/`)

- [x] HOCON parser integration (`github.com/gurkankaymak/hocon`)
- [x] Parse config reference: sources, tables, pipelines, targets, buffer, error handling, TTL, snapshot, gRPC
- [x] Config-to-engine-options mapper: translate parsed config into `laredo.Option` calls
- [x] Source factory: map `type = postgresql` → `pg.New()`
- [x] Target factory: map `type = indexed-memory`, `compiled-memory`, `http-sync`
- [x] Filter/transform factory: map `type = field-equals`, `field-prefix`, `drop-fields`, `add-timestamp`
- [x] Environment variable override: `SOURCES_PG_MAIN_CONNECTION` / `LAREDO_SOURCES_PG_MAIN_CONNECTION`
- [x] Config directory merge: load `conf.d/*.conf` in alphabetical order
- [x] `--set key=value` CLI flag override
- [x] Config validation: required fields, type checking, cross-references (source IDs in table config must exist)
- [x] Config dump command for debugging (`laredo-server config --config <path>`)
- [x] Sensitive value masking in dumps/logs (connection strings, auth headers)

---

## 8. gRPC Services (`service/`)

### 8.1 Protobuf Definitions

- [x] `proto/laredo/v1/oam.proto` — full OAM service from spec §17.1 (all messages, all RPCs)
- [x] `proto/laredo/v1/query.proto` — full Query service from spec §17.2
- [x] `proto/laredo/replication/v1/replication.proto` — full Replication service from spec §6.4.9
- [x] `buf.yaml` + `buf.gen.yaml` for buf-managed code generation
- [x] Generate Go code into `gen/` (committed to repo)
- [x] Add `make proto` target (already in Makefile, needs buf config)

### 8.2 OAM Service (`service/oam/`)

- [ ] `GetStatus` — aggregate engine state, source statuses, pipeline statuses
- [ ] `GetTableStatus` — pipelines + indexes for a table
- [ ] `GetPipelineStatus` — single pipeline status + indexes
- [ ] `WatchStatus` — server-streaming status events (state changes, row changes, source events)
- [x] `CheckReady` — readiness check (global, per-source, per-table, per-pipeline)
- [ ] `GetSourceInfo` — source details including source-specific metadata
- [x] `ReloadTable` / `ReloadAll` — trigger re-baseline
- [x] `PauseSync` / `ResumeSync`
- [ ] `ResetSource` — drop/recreate slot and optionally publication
- [ ] `ListTables` / `GetTableSchema` — read-only config inspection
- [x] `CreateSnapshot` / `ListSnapshots` / `InspectSnapshot` / `RestoreSnapshot` / `DeleteSnapshot` / `PruneSnapshots` (`RestoreSnapshot` deferred — needs engine method)
- [x] `ListDeadLetters` / `ReplayDeadLetters` / `PurgeDeadLetters` (`ListDeadLetters` + `PurgeDeadLetters` implemented; `ReplayDeadLetters` deferred — needs target lookup by pipeline ID)
- [x] `StartReplay` / `GetReplayStatus` / `StopReplay`

### 8.3 Query Service (`service/query/`)

- [x] `Lookup` — single-row lookup on unique index
- [x] `LookupAll` — multi-row lookup on non-unique index
- [x] `GetRow` — direct primary key access
- [x] `ListRows` — paginated listing
- [x] `CountRows`
- [ ] `Subscribe` — server-streaming change events with optional replay of existing rows

### 8.4 Server Setup (`service/server.go`)

- [ ] TLS configuration
- [x] Register OAM + Query services
- [x] Optional service enabling (`EnableOAM`, `EnableQuery`)
- [x] Port configuration
- [x] Graceful shutdown: stop accepting, drain in-flight requests

---

## 9. Observability (`metrics/`)

### 9.1 Prometheus (`metrics/prometheus/`)

- [x] Implement full `EngineObserver` interface
- [x] Gauges: pipeline state, buffer depth, row count, lag bytes, lag time, connected fan-out clients
- [x] Counters: inserts/updates/deletes/truncates applied (per pipeline), changes received, errors, dead letters written, rows expired, snapshots created/failed
- [x] Histograms: change apply duration, baseline duration, snapshot duration
- [x] HTTP `/metrics` endpoint handler
- [x] Metric naming convention: `laredo_pipeline_row_count`, `laredo_source_lag_bytes`, etc.

### 9.2 OpenTelemetry (`metrics/otel/`)

- [x] Implement full `EngineObserver` interface
- [x] Map same metrics to OTel meter API
- [ ] Configurable exporter (OTLP, stdout, etc.)

---

## 10. CLI (`cmd/laredo/`)

- [x] CLI framework (flag-based subcommand dispatcher)
- [x] Global flags: `--address`, `--timeout`, `--output` (table/json)
- [x] `LAREDO_ADDRESS` env var for default server address

### 10.1 Commands (spec §18)

- [ ] `laredo status` — overall service status (sources, pipelines, buffers)
- [ ] `laredo status --table <schema.table>` — per-table detail with indexes
- [ ] `laredo source [source_id]` — source detail including source-specific metadata (slot info, shard info)
- [ ] `laredo pipelines` — tabular list of all pipelines
- [ ] `laredo tables` — tabular list of configured tables
- [ ] `laredo schema <schema.table>` — column definitions
- [ ] `laredo query <schema.table> [key_values...]` — lookup by primary index
- [ ] `laredo query --index <name> [key_values...]` — lookup by named index
- [x] `laredo query --pk <id>` — lookup by primary key
- [ ] `laredo query --all --limit N` — paginated listing
- [ ] `laredo watch [schema.table]` — stream status events / row changes
- [ ] `laredo watch --verbose` — include full row data
- [x] `laredo reload <schema.table>` / `laredo reload --all` / `laredo reload --source <id> --all`
- [x] `laredo pause [--source <id>]` / `laredo resume [--source <id>]`
- [ ] `laredo reset-source <source_id>` with confirmation prompt
- [ ] `laredo reset-source <source_id> --drop-publication` for decommissioning
- [x] `laredo snapshot create [--meta key=value ...]`
- [x] `laredo snapshot list`
- [x] `laredo snapshot inspect <id>`
- [ ] `laredo snapshot restore <id>` with confirmation prompt
- [x] `laredo snapshot delete <id>`
- [x] `laredo snapshot prune --keep N`
- [x] `laredo dead-letters <pipeline_id>` — list dead letters
- [ ] `laredo dead-letters replay <pipeline_id>`
- [x] `laredo dead-letters purge <pipeline_id>`
- [ ] `laredo replay <snapshot_id> [--pipeline <id>] [--speed full|realtime|Nx]`
- [x] `laredo ready [--pipeline <id>]` — exit code 0 if ready, 1 if not
- [ ] `laredo fanout status <schema.table>` — replication fan-out status
- [ ] `laredo fanout clients <schema.table>` — connected clients
- [ ] `laredo fanout snapshots <schema.table>` — available fan-out snapshots
- [ ] `laredo fanout journal <schema.table> --tail N` — recent journal entries

### 10.2 Output Formatting

- [ ] Table formatter: aligned columns, human-readable numbers (1,284), durations (3h 42m)
- [x] JSON output: structured, machine-readable
- [ ] YAML output

---

## 11. Pre-built Service (`cmd/laredo-server/`)

- [x] Config loading: file via --config flag or LAREDO_CONFIG env var
- [x] Config validation on startup (fail fast with clear errors)
- [x] Engine construction from config
- [x] gRPC server startup (OAM + Query on configured port)
- [x] Health HTTP server:
  - [x] `/health/live` — process is running
  - [x] `/health/ready` — all pipelines streaming (JSON body)
  - [x] `/health/startup` — service initialized
- [x] Metrics endpoint (Prometheus `/metrics`)
- [x] Structured logging (slog-based JSON, configurable level via --log-level)
- [x] Signal handling: `SIGTERM` / `SIGINT` → graceful shutdown
- [x] Graceful shutdown sequence: stop gRPC → stop health → stop engine
- [x] Configurable shutdown timeout (`WithShutdownTimeout`)
- [x] `laredo-server validate` subcommand: validate config without starting
- [ ] `laredo-server config --dump` subcommand: print merged config
- [x] `laredo-server healthcheck` subcommand: hit local health endpoint, exit 0/1

---

## 12. Fan-Out Client Library (`client/fanout/`)

- [ ] `Client` struct with options: `ServerAddress()`, `Table()`, `ClientID()`
- [ ] `LocalSnapshotPath()` — optional local snapshot cache for fast restart
- [ ] `WithIndexedState()` — configure client-side indexed in-memory store
- [ ] `Client.Start()` — connect, receive snapshot/delta, populate local state
- [ ] `Client.AwaitReady(timeout)` — block until initial state loaded
- [ ] `Client.Lookup(keyValues...)` / `Client.LookupAll(indexName, keyValues...)`
- [ ] `Client.Listen(func(old, new Row))` — subscribe to changes
- [ ] `Client.Stop()` — save local snapshot, disconnect
- [ ] Auto-reconnect with exponential backoff
- [ ] Heartbeat timeout detection (no message for 30s → reconnect)
- [ ] Local snapshot save/restore on start/stop
- [ ] gRPC client implementation for `TableSyncReplication` service

---

## 13. Docker & Deployment

### 13.1 Docker Image

- [ ] Multi-stage build (already scaffolded, needs refinement)
- [ ] Distroless base image for production, alpine variant for debug
- [ ] Entrypoint wrapper: run init scripts from `/docker-entrypoint-init.d/`, merge config, validate, start
- [ ] HEALTHCHECK instruction
- [ ] Exposed ports: 4001 (gRPC), 4002 (replication), 8080 (health), 9090 (metrics)
- [ ] Volumes: `/etc/laredo/`, `/etc/laredo/conf.d/`, `/var/lib/laredo/`, `/docker-entrypoint-init.d/`

### 13.2 GoReleaser (already scaffolded)

- [ ] Verify cross-compilation: linux/darwin amd64/arm64
- [ ] Docker image push to `ghcr.io/zourzouvillys/laredo-server`
- [ ] Checksums, SBOMs
- [ ] Changelog generation

### 13.3 GitHub Actions

- [ ] CI: lint + test + build (already scaffolded)
- [ ] Release: GoReleaser on tag push (already scaffolded)
- [ ] Add integration test job (requires PostgreSQL service container)
- [ ] Add documentation build/deploy job (GitHub Pages)
- [ ] Code coverage reporting

---

## 14. Testing

### 14.1 Unit Tests (Tier 1 — per package `_test.go`)

- [x] `types_test.go` — Row.GetString, Row.Without, TableIdentifier.String, ChangeAction.String
- [x] `pipeline_test.go` — PipelineFilterFunc, PipelineTransformFunc, PipelineState.String
- [x] `errors_test.go` — ValidationError.Error
- [x] `filter/filter_test.go` — FieldEquals, FieldPrefix, FieldRegex
- [x] `transform/transform_test.go` — DropFields, RenameFields, AddTimestamp
- [x] `snapshot/jsonl/jsonl_test.go` — round-trip serialize/deserialize
- [x] `snapshot/local/local_test.go` — save/load/list/delete/prune with temp directory
- [x] `internal/engine/buffer_test.go` — all three policies, concurrent access
- [x] `internal/engine/ack_test.go` — minimum position tracking across pipelines
- [x] `internal/engine/readiness_test.go` — state transitions, await with timeout, OnReady callbacks
- [x] `target/memory/memory_test.go` — IndexedTarget + CompiledTarget: full coverage
- [x] `deadletter/memory_test.go` + `deadletter/local_test.go` — write/read/replay/purge
- [ ] `target/httpsync/httpsync_test.go` — batching, flush interval, retry, durability tracking (use httptest)
- [ ] `target/fanout/journal_test.go` — circular buffer, pruning, sequence tracking
- [ ] `target/fanout/fanout_test.go` — full lifecycle, client protocol
- [ ] `config/config_test.go` — parse HOCON, env var override, validation errors
- [x] `deadletter/memory_test.go` + `deadletter/local_test.go` — write/read/replay/purge
- [ ] `service/oam/oam_test.go` — gRPC service handlers (mock engine)
- [ ] `service/query/query_test.go` — gRPC service handlers (mock engine)
- [ ] `client/fanout/client_test.go` — connect, snapshot/delta, reconnect

### 14.2 Integration Tests (Tier 2 — `test/integration/`)

- [ ] PostgreSQL source: ephemeral mode — full baseline + streaming against real PG (testcontainers)
- [ ] PostgreSQL source: stateful mode — baseline, stream, restart, resume from LSN
- [ ] PostgreSQL source: publication management — auto-create, add/remove tables, row filters (PG 15+)
- [ ] PostgreSQL source: reconnect — simulate connection loss, verify reconnect and resume
- [ ] PostgreSQL source: slot invalidation — exceed `max_slot_wal_keep_size`, verify re-baseline
- [ ] Engine + PG source + indexed memory target: end-to-end pipeline
- [ ] Engine + PG source + HTTP target: end-to-end with httptest server
- [ ] Engine + PG source + fan-out target + fan-out client: full replication chain
- [x] Snapshot create + restore cycle with real engine (local disk store, full round-trip)
- [x] Multi-pipeline ACK coordination: verify minimum position across targets (TestEngine_AckCoordination)
- [x] Error isolation: one pipeline fails, others continue (TestEngine_ErrorPolicyIsolate)
- [x] Dead letter: verify failed changes written to DLQ and replayable (TestEngine_DeadLetterIntegration)
- [x] TTL: insert rows with expiry, verify automatic removal (TestEngine_TTLPeriodicScanner)

### 14.3 End-to-End Tests (Tier 3 — `test/e2e/`)

- [ ] Full laredo-server startup from HOCON config against real PG
- [ ] gRPC OAM commands via CLI: status, reload, pause/resume, snapshot
- [ ] gRPC Query commands via CLI: lookup, list, count, subscribe
- [ ] Fan-out: multiple clients connect, receive consistent state, live updates
- [ ] Graceful shutdown: verify snapshot-on-shutdown, clean restart with resume
- [ ] Docker container: build image, start with docker-compose (PG + laredo-server), verify health endpoints

### 14.4 Benchmarks (Tier 4 — `test/bench/`)

- [ ] Indexed in-memory target: insert throughput, lookup latency (1K, 100K, 1M rows)
- [ ] Compiled in-memory target: compile + insert throughput
- [ ] Change buffer throughput: all three policies under contention
- [ ] Snapshot serialization: JSONL write/read speed for large datasets
- [ ] Fan-out broadcast: throughput with 1, 10, 100 connected clients
- [ ] PostgreSQL source: baseline rows/sec, streaming changes/sec

### 14.5 Test Infrastructure

- [x] `test/testutil/observer.go` — TestObserver records all 21 observer callbacks
- [x] `test/testutil/helpers.go` — SampleTable, SampleColumns, SampleRow, SampleChangeEvent, AssertEventually
- [ ] `test/testutil/pg.go` — PostgreSQL testcontainer setup/teardown helper
- [ ] `test/testutil/grpc.go` — in-process gRPC server/client for service tests
- [ ] `test/testutil/httpserver.go` — configurable mock HTTP server for target tests
- [ ] Test data generators: random rows, schemas, change events

---

## 15. Documentation (GitHub Pages)

### 15.1 Site Infrastructure

- [ ] Choose static site generator (Hugo, Docusaurus, or mkdocs-material)
- [ ] `docs/` directory structure for documentation source
- [ ] GitHub Actions workflow: build docs on push to main, deploy to GitHub Pages
- [ ] Custom domain setup (optional)
- [ ] Site navigation: sidebar, search, versioning

### 15.2 Getting Started

- [ ] Landing page: what is Laredo, key features, architecture diagram
- [ ] Quick start guide: install binary, start with PG + in-memory target in under 5 minutes
- [ ] Docker quick start: docker-compose with PG + laredo-server
- [ ] Library quick start: embed in a Go application

### 15.3 Concepts

- [ ] Architecture overview: core library vs modules vs pre-built service
- [ ] Pipeline model: source → filter → transform → target
- [ ] Sources: what they do, how they connect
- [ ] Targets: what they do, when to use each type
- [ ] Snapshots: what they are, startup paths, scheduling
- [ ] ACK coordination: how positions are tracked across fan-out pipelines
- [ ] Ordering guarantees: total order vs per-partition vs best-effort
- [ ] Delivery guarantees: at-least-once semantics, idempotency requirements

### 15.4 Guides

- [ ] Configuring PostgreSQL logical replication (ephemeral vs stateful mode)
- [ ] Setting up publication management (auto-create, row filters, column lists)
- [ ] Using indexed in-memory targets with secondary indexes
- [ ] Using compiled in-memory targets with custom compilers
- [ ] Setting up HTTP sync targets (batching, retry, durability)
- [ ] Setting up replication fan-out (server + client)
- [ ] Fan-out client library usage (Go)
- [ ] Pipeline filters and transforms
- [ ] Error handling and dead letter queues
- [ ] TTL / expiry configuration
- [ ] Snapshot management (scheduling, retention, restore)
- [ ] Monitoring with Prometheus / Grafana dashboards
- [ ] Monitoring with OpenTelemetry
- [ ] Deploying with Docker
- [ ] Deploying on Kubernetes (Deployment + ConfigMap + Secrets + health probes)
- [ ] Custom source implementation guide
- [ ] Custom target implementation guide

### 15.5 Reference

- [ ] Full HOCON configuration reference (every key, type, default, description)
- [ ] CLI reference: every command, flag, env var, example
- [ ] gRPC API reference: OAM service (every RPC, request/response)
- [ ] gRPC API reference: Query service
- [ ] gRPC API reference: Replication service
- [ ] Go library API reference (godoc-generated or hand-written)
- [ ] HTTP health endpoints reference
- [ ] Metrics reference: every metric name, type, labels, description
- [ ] Environment variables reference
- [ ] Error codes reference (validation errors, gRPC status codes)

### 15.6 Operations

- [ ] Runbook: handling slot lag warnings
- [ ] Runbook: recovering from slot invalidation
- [ ] Runbook: dealing with dead letters
- [ ] Runbook: forcing a re-baseline
- [ ] Runbook: resetting a source (decommissioning)
- [ ] Runbook: restoring from a snapshot after data loss
- [ ] Runbook: scaling fan-out clients
- [ ] Troubleshooting guide: common error messages and resolutions

### 15.7 Design & Internals

- [ ] Design specification (link to `docs/spec.md` or rendered version)
- [ ] Architecture decision records (ADRs) for key choices
- [ ] Contributing guide
- [ ] Changelog / release notes process

---

## 16. Release Readiness

### 16.1 API Stability

- [ ] Review all exported types/interfaces for Go API compatibility promises
- [ ] Ensure no unintentional public API surface (move internal types to `internal/`)
- [ ] Document stability guarantees per package (stable, experimental, internal)
- [ ] Consider adding `go doc` examples for key public functions

### 16.2 Security

- [ ] Audit for credential leakage in logs/metrics/error messages
- [ ] TLS support for gRPC server and client connections
- [ ] Connection string handling: never log full connection strings
- [ ] Auth header handling: never log auth tokens
- [ ] Dependency audit: run `govulncheck`

### 16.3 Performance

- [ ] Profile baseline loading with large tables (1M+ rows)
- [ ] Profile streaming throughput (sustained changes/sec)
- [ ] Profile memory usage for in-memory targets at scale
- [ ] Optimize hot paths identified by benchmarks
- [ ] Document resource requirements and sizing guidance

### 16.4 Packaging

- [ ] `go.sum` — populated once dependencies are added
- [ ] License file (choose license)
- [ ] `CONTRIBUTING.md`
- [ ] `CHANGELOG.md` — start tracking changes
- [ ] GitHub repository settings: branch protection, required checks, release drafts
- [ ] Go module proxy: verify `go install github.com/zourzouvillys/laredo/cmd/laredo@latest` works after first tag
