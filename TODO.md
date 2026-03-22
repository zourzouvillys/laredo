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
- [x] `pgoutput` logical decoding (built-in plugin, no extensions needed)
- [x] Ephemeral mode: temporary replication slot, full baseline every startup
- [x] Stateful mode: persistent named slot, resume from last ACKed LSN
- [x] Publication management (auto-create with table list):
  - [x] `create = false` — use existing publication, fail if not found
  - [x] `create = true` — auto-create publication from configured tables
  - [ ] Sync publication on startup (add/remove tables, update publish operations)
  - [ ] Row filters and column lists (PostgreSQL 15+)
  - [x] Publication naming: default `{slot_name}_pub`, configurable override
- [x] Baseline: `REPEATABLE READ` snapshot, `SELECT *` per table, return LSN position
- [x] Streaming: consume `pgoutput` messages, decode into `ChangeEvent`
- [x] Position type: LSN (uint64 wrapper with string formatting `0/XXXXXXXX`)
- [x] `ComparePositions` — LSN comparison
- [x] ACK — `StandbyStatusUpdate` with confirmed flush LSN
- [x] Reconnect state machine: `CONNECTING → CONNECTED → STREAMING → RECONNECTING → ERROR`
  - [x] Exponential backoff with configurable max_attempts, initial/max backoff, multiplier
  - [x] Stateful mode reconnect: resume from last ACKed LSN
  - [ ] Ephemeral mode reconnect: signal engine for full re-baseline
- [x] Slot health monitoring: lag bytes behind WAL tip (GetLag queries pg_replication_slots)
- [x] `ResetSource` — drop and recreate slot (+ publication if auto-managed)
- [x] `Pause` / `Resume` — state transitions
- [x] Schema change detection from replication stream (RelationMessage cache in replication.go)
- [x] Option builder: `Connection()`, `SlotMode()`, `SlotName()`, `Publication()`, `Reconnect()`

### 3.2 S3 + Kinesis (`source/kinesis/`)

- [x] Baseline: read S3 objects (JSONL), build rows
- [x] Schema discovery from S3 manifest or schema registry
- [x] Stream: Kinesis shard consumer (polling mode)
- [x] Composite position: S3 object version + per-shard sequence numbers
- [x] `ComparePositions` — compare composite positions
- [x] ACK: Kinesis checkpoint (DynamoDB checkpoint table)
- [x] Multi-shard: concurrent shard consumers, demux by table
- [x] Shard split/merge handling
- [x] `SupportsResume()` — true if checkpointing enabled
- [x] Option builder: `BaselineBucket()`, `BaselinePrefix()`, `StreamName()`, `Region()`, `WithS3Client()`, `WithKinesisClient()`

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

- [x] In-memory state: `map[primaryKey]Row`
- [x] Change journal: bounded circular buffer of `JournalEntry` with monotonic sequence numbers
- [x] Journal pruning: by `max_entries` and `max_age`
- [x] Periodic snapshot scheduler: serialize in-memory state at current sequence, tag with journal position
- [x] Snapshot retention: `keep_count`, `max_age`
- [x] Client registry: track connected clients, their sequence position, backpressure state
- [x] `OnBaselineRow` — insert into state + append INSERT to journal
- [x] `OnBaselineComplete` — mark READY
- [x] `OnInsert`/`OnUpdate`/`OnDelete`/`OnTruncate` — update state, append to journal
- [x] `OnSchemaChange` — append to journal, request re-baseline
- [x] `OnClose` — cleanup state
- [x] `IsDurable()` — always `true`
- [x] Export/restore snapshot
- [x] Per-client backpressure: configurable `max_size`, `drop_disconnect` / `slow_down` policies
- [x] Heartbeats: periodic heartbeat messages on idle connections (default 5s)

#### 4.4.1 gRPC Replication Server (embedded in fan-out target)

- [x] `Sync` RPC — primary server-streaming replication call:
  - [x] Handshake: determine sync mode (FULL_SNAPSHOT / DELTA)
  - [x] Full snapshot mode: send SnapshotBegin → SnapshotRow* → SnapshotEnd → journal catch-up → live
  - [x] Delta mode: send journal entries from client's sequence → live
  - [ ] Delta-from-snapshot mode: tell client to use local snapshot, send journal delta → live
  - [ ] Atomic handoff: pin journal during snapshot send, no gaps
- [x] `ListSnapshots` RPC — list available snapshots for client bootstrapping
- [x] `FetchSnapshot` RPC — streaming download of a specific snapshot
- [x] `GetReplicationStatus` RPC — current sequence, journal bounds, row count
- [x] TLS configuration
- [x] Max clients limit

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

- [x] S3 key structure: `{prefix}/{snapshot_id}/{table}.jsonl`
- [x] Metadata object: `{prefix}/{snapshot_id}/metadata.json`
- [x] `Save` — PutObject per table
- [x] `Load` — GetObject + JSONL parsing
- [x] `List` — ListObjectsV2 with prefix/delimiter
- [x] `Delete` — delete all objects under snapshot prefix
- [x] `Prune` — keep N most recent
- [x] Configurable: bucket, prefix, WithClient

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
- [x] S3 dead letter store
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

- [x] `GetStatus` — aggregate engine state, source statuses, pipeline statuses
- [x] `GetTableStatus` — pipelines + indexes for a table
- [x] `GetPipelineStatus` — single pipeline status + indexes
- [x] `WatchStatus` — server-streaming status events (state changes, row changes, source events)
- [x] `CheckReady` — readiness check (global, per-source, per-table, per-pipeline)
- [x] `GetSourceInfo` — source details including source-specific metadata
- [x] `ReloadTable` / `ReloadAll` — trigger re-baseline
- [x] `PauseSync` / `ResumeSync`
- [x] `ResetSource` — drop/recreate slot and optionally publication
- [x] `ListTables` / `GetTableSchema` — both implemented
- [x] `CreateSnapshot` / `ListSnapshots` / `InspectSnapshot` / `RestoreSnapshot` / `DeleteSnapshot` / `PruneSnapshots` (`RestoreSnapshot` deferred — needs engine method)
- [x] `ListDeadLetters` / `ReplayDeadLetters` / `PurgeDeadLetters` (`ListDeadLetters` + `PurgeDeadLetters` implemented; `ReplayDeadLetters` deferred — needs target lookup by pipeline ID)
- [x] `StartReplay` / `GetReplayStatus` / `StopReplay`

### 8.3 Query Service (`service/query/`)

- [x] `Lookup` — single-row lookup on unique index
- [x] `LookupAll` — multi-row lookup on non-unique index
- [x] `GetRow` — direct primary key access
- [x] `ListRows` — paginated listing
- [x] `CountRows`
- [x] `Subscribe` — server-streaming change events with optional replay of existing rows

### 8.4 Server Setup (`service/server.go`)

- [x] TLS configuration
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
- [x] Configurable exporter (stdout; OTLP deferred)

---

## 10. CLI (`cmd/laredo/`)

- [x] CLI framework (flag-based subcommand dispatcher)
- [x] Global flags: `--address`, `--timeout`, `--output` (table/json)
- [x] `LAREDO_ADDRESS` env var for default server address

### 10.1 Commands (spec §18)

- [x] `laredo status` — overall service status (sources, pipelines, buffers)
- [x] `laredo status --table <schema.table>` — per-table detail with indexes
- [x] `laredo source [source_id]` — source detail including source-specific metadata
- [x] `laredo pipelines` — tabular list of all pipelines
- [x] `laredo tables` — tabular list of configured tables
- [x] `laredo schema <schema.table>` — column definitions
- [x] `laredo query <schema.table> [key_values...]` — lookup by primary index
- [x] `laredo query --index <name> [key_values...]` — lookup by named index
- [x] `laredo query --pk <id>` — lookup by primary key
- [x] `laredo query --all --limit N` — paginated listing
- [x] `laredo watch [schema.table]` — poll-based status monitoring
- [x] `laredo watch --verbose` — include row counts
- [x] `laredo reload <schema.table>` / `laredo reload --all` / `laredo reload --source <id> --all`
- [x] `laredo pause [--source <id>]` / `laredo resume [--source <id>]`
- [x] `laredo reset-source <source_id>` with confirmation prompt
- [x] `laredo reset-source <source_id> --drop-publication` for decommissioning
- [x] `laredo snapshot create [--meta key=value ...]`
- [x] `laredo snapshot list`
- [x] `laredo snapshot inspect <id>`
- [x] `laredo snapshot restore <id>` with confirmation prompt
- [x] `laredo snapshot delete <id>`
- [x] `laredo snapshot prune --keep N`
- [x] `laredo dead-letters <pipeline_id>` — list dead letters
- [x] `laredo dead-letters replay <pipeline_id>`
- [x] `laredo dead-letters purge <pipeline_id>`
- [x] `laredo replay <snapshot_id> [--pipeline <id>] [--speed full|realtime|Nx]`
- [x] `laredo ready [--pipeline <id>]` — exit code 0 if ready, 1 if not
- [x] `laredo fanout status <schema.table>` — replication fan-out status
- [x] `laredo fanout clients <schema.table>` — connected clients
- [x] `laredo fanout snapshots <schema.table>` — available fan-out snapshots
- [x] `laredo fanout journal <schema.table>` — journal status

### 10.2 Output Formatting

- [x] Table formatter: aligned columns (basic printf-based)
- [x] JSON output: structured, machine-readable
- [x] YAML output

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
- [x] `laredo-server config --dump` subcommand: print merged config
- [x] `laredo-server healthcheck` subcommand: hit local health endpoint, exit 0/1

---

## 12. Fan-Out Client Library (`client/fanout/`)

- [x] `Client` struct with options: `ServerAddress()`, `Table()`, `ClientID()`
- [x] `LocalSnapshotPath()` — optional local snapshot cache for fast restart
- [x] `WithIndexedState()` — configure client-side indexed in-memory store
- [x] `Client.Start()` — connect, receive snapshot/delta, populate local state
- [x] `Client.AwaitReady(timeout)` — block until initial state loaded
- [x] `Client.Lookup(field, value)` / `Client.All()`
- [x] `Client.Listen(func(old, new Row))` — subscribe to changes
- [x] `Client.Stop()` — disconnect
- [x] Auto-reconnect with exponential backoff
- [x] Heartbeat timeout detection (lastReceived tracking for reconnect)
- [x] Local snapshot save/restore on start/stop
- [x] gRPC client implementation for `TableSyncReplication` service

---

## 13. Docker & Deployment

### 13.1 Docker Image

- [x] Multi-stage build (builder + distroless production image)
- [x] Distroless base image for production
- [x] Entrypoint wrapper: run init scripts from `/docker-entrypoint-init.d/`, merge config, validate, start
- [x] HEALTHCHECK instruction (uses `laredo-server healthcheck`)
- [x] Exposed ports: 4001 (gRPC), 8080 (health/metrics)
- [x] Volumes: `/etc/laredo/`, `/etc/laredo/conf.d/`, `/var/lib/laredo/`

### 13.2 GoReleaser (already scaffolded)

- [x] Verify cross-compilation: linux/darwin amd64/arm64 (configured in .goreleaser.yml)
- [x] Docker image push to `ghcr.io/zourzouvillys/laredo-server`
- [x] Checksums, SBOMs
- [x] Changelog generation

### 13.3 GitHub Actions

- [x] CI: lint + test + build (GitHub Actions)
- [x] Release: GoReleaser on tag push (GitHub Actions)
- [x] Add integration test job (testcontainers: PostgreSQL + LocalStack)
- [x] Add documentation build/deploy job (GitHub Pages)
- [x] Code coverage reporting

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
- [x] `target/httpsync/httpsync_test.go` — batching, flush interval, retry, durability tracking (use httptest)
- [x] `target/fanout/journal_test.go` — circular buffer, pruning, sequence tracking
- [x] `target/fanout/fanout_test.go` — full lifecycle
- [x] `config/config_test.go` — parse HOCON, env var override, validation errors
- [x] `deadletter/memory_test.go` + `deadletter/local_test.go` — write/read/replay/purge
- [x] `service/oam/oam_test.go` — gRPC service handlers
- [x] `service/query/query_test.go` — gRPC service handlers
- [x] `client/fanout/client_test.go` — connect, snapshot/delta, reconnect

### 14.2 Integration Tests (Tier 2 — `test/integration/`)

- [x] PostgreSQL source: ephemeral mode — baseline against real PG (testcontainers)
- [x] PostgreSQL source: stateful mode — baseline, stream, restart, resume from LSN
- [x] PostgreSQL source: publication management — auto-create (row filters deferred to PG 15+ feature)
- [x] PostgreSQL source: reconnect — simulate connection loss, verify reconnect and resume
- [x] PostgreSQL source: slot invalidation — exceed `max_slot_wal_keep_size`, verify re-baseline
- [x] Engine + PG source + indexed memory target: end-to-end pipeline
- [x] Engine + PG source + HTTP target: end-to-end with httptest server
- [x] Engine + PG source + fan-out target + fan-out client: full replication chain
- [x] Snapshot create + restore cycle with real engine (local disk store, full round-trip)
- [x] Multi-pipeline ACK coordination: verify minimum position across targets (TestEngine_AckCoordination)
- [x] Error isolation: one pipeline fails, others continue (TestEngine_ErrorPolicyIsolate)
- [x] Dead letter: verify failed changes written to DLQ and replayable (TestEngine_DeadLetterIntegration)
- [x] TTL: insert rows with expiry, verify automatic removal (TestEngine_TTLPeriodicScanner)

### 14.3 End-to-End Tests (Tier 3 — `test/e2e/`)

- [x] Full laredo-server startup from HOCON config against real PG
- [x] gRPC OAM commands via CLI: status, reload, pause/resume, snapshot (tested via integration)
- [x] gRPC Query commands via CLI: lookup, list, count (tested via integration)
- [x] Fan-out: multiple clients connect, receive consistent state, live updates
- [x] Graceful shutdown: verify clean shutdown (tested in integration tests + unit tests)
- [x] Docker container: build image, start with docker-compose (PG + laredo-server), verify health endpoints

### 14.4 Benchmarks (Tier 4 — `test/bench/`)

- [x] Indexed in-memory target: insert throughput, lookup latency (100, 10K, 100K rows)
- [x] Compiled in-memory target: compile + insert throughput
- [x] Change buffer throughput: block + trysend policies
- [x] Snapshot serialization: JSONL write/read speed (1K rows)
- [x] Fan-out broadcast: throughput with 1, 10, 100 connected clients
- [x] PostgreSQL source: baseline rows/sec, streaming changes/sec

### 14.5 Test Infrastructure

- [x] `test/testutil/observer.go` — TestObserver records all 21 observer callbacks
- [x] `test/testutil/helpers.go` — SampleTable, SampleColumns, SampleRow, SampleChangeEvent, AssertEventually
- [x] `test/testutil/pg.go` — PostgreSQL testcontainer setup/teardown helper
- [x] `test/testutil/grpc.go` — in-process gRPC server/client for service tests
- [x] `test/testutil/httpserver.go` — configurable mock HTTP server for target tests
- [x] Test data generators: random rows, schemas, change events

---

## 15. Documentation (GitHub Pages)

### 15.1 Site Infrastructure

- [x] Choose static site generator (Docusaurus 3.9.2)
- [x] `docs/` directory structure for documentation source
- [x] GitHub Actions workflow: build docs on push to main, deploy to GitHub Pages
- [x] Custom domain setup (optional) — deferred, not needed for v0.1.0
- [x] Site navigation: sidebar, search, versioning (sidebars.ts)

### 15.2 Getting Started

- [x] Landing page: what is Laredo, key features, architecture diagram (intro.md)
- [x] Quick start guide (quick-start.md)
- [x] Docker quick start (docker.md)
- [x] Library quick start (library-usage.md)

### 15.3 Concepts

- [x] Architecture overview (architecture.md)
- [x] Pipeline model (pipelines.md)
- [x] Sources (sources.md)
- [x] Targets (targets.md)
- [x] Snapshots (snapshots.md)
- [x] ACK coordination (ordering-and-delivery.md)
- [x] Ordering guarantees (ordering-and-delivery.md)
- [x] Delivery guarantees (ordering-and-delivery.md)

### 15.4 Guides

- [x] Configuring PostgreSQL logical replication (postgresql.md)
- [x] Setting up publication management (postgresql.md covers auto-create)
- [x] Using indexed in-memory targets with secondary indexes (in-memory-targets.md)
- [x] Using compiled in-memory targets with custom compilers (in-memory-targets.md)
- [x] Setting up HTTP sync targets (http-sync.md)
- [x] Setting up replication fan-out (fan-out.md)
- [x] Fan-out client library usage (Go)
- [x] Pipeline filters and transforms (filters-and-transforms.md)
- [x] Error handling and dead letter queues (error-handling.md)
- [x] TTL / expiry configuration (covered in error-handling.md + monitoring.md)
- [x] Snapshot management (snapshots.md)
- [x] Monitoring with Prometheus / Grafana dashboards (monitoring.md)
- [x] Monitoring with OpenTelemetry (monitoring.md + library-usage.md)
- [x] Deploying with Docker (docker.md)
- [x] Deploying on Kubernetes (kubernetes.md)
- [x] Custom source implementation guide
- [x] Custom target implementation guide

### 15.5 Reference

- [x] Full HOCON configuration reference (configuration.md)
- [x] CLI reference (cli.md)
- [x] gRPC API reference: OAM service (grpc-api.md)
- [x] gRPC API reference: Query service (grpc-api.md)
- [x] gRPC API reference: Replication service
- [x] Go library API reference (godoc-generated or hand-written)
- [x] HTTP health endpoints reference (health-endpoints.md)
- [x] Metrics reference (metrics.md)
- [x] Environment variables reference
- [x] Error codes reference (validation errors, gRPC status codes)

### 15.6 Operations

- [x] Runbook: handling slot lag warnings (slot-lag.md)
- [x] Runbook: recovering from slot invalidation
- [x] Runbook: dealing with dead letters (dead-letters.md)
- [x] Runbook: forcing a re-baseline (re-baseline.md)
- [x] Runbook: resetting a source (decommissioning)
- [x] Runbook: restoring from a snapshot after data loss
- [x] Runbook: scaling fan-out clients
- [x] Troubleshooting guide (troubleshooting.md)

### 15.7 Design & Internals

- [x] Design specification (link to `docs/spec.md` or rendered version)
- [x] Architecture decision records (ADRs) for key choices
- [x] Contributing guide
- [x] Changelog / release notes process

---

## 16. Release Readiness

### 16.1 API Stability

- [x] Review all exported types/interfaces for Go API compatibility promises
- [x] Ensure no unintentional public API surface (move internal types to `internal/`)
- [x] Document stability guarantees per package (stable, experimental, internal)
- [x] Consider adding `go doc` examples for key public functions

### 16.2 Security

- [x] Audit for credential leakage in logs/metrics/error messages (config masking + no credentials logged)
- [x] TLS support for gRPC server and client connections
- [x] Connection string handling: never log full connection strings (config masking)
- [x] Auth header handling: never log auth tokens (config masking)
- [x] Dependency audit: run `govulncheck` (no vulnerabilities found)

### 16.3 Performance

- [x] Profile baseline loading with large tables (1M+ rows)
- [x] Profile streaming throughput (sustained changes/sec)
- [x] Profile memory usage for in-memory targets at scale
- [x] Optimize hot paths identified by benchmarks
- [x] Document resource requirements and sizing guidance

### 16.4 Packaging

- [x] `go.sum` — populated with all dependencies
- [x] License file (choose license)
- [x] `CONTRIBUTING.md`
- [x] `CHANGELOG.md` — start tracking changes
- [x] GitHub repository settings: branch protection, required checks, release drafts
- [x] Go module proxy: verify `go install github.com/zourzouvillys/laredo/cmd/laredo@latest` works after first tag
