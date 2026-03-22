---
sidebar_position: 3
title: gRPC API
---

# gRPC API Reference

Laredo exposes three gRPC services. OAM and Query share a single port (default 4001). The Replication service runs on a separate port per fan-out target (default 4002).

All services use Connect (compatible with gRPC, gRPC-Web, and Connect protocols).

## OAM Service (`laredo.v1.TableSyncOAM`)

Port: 4001 (default, configurable via `grpc.port`)

### Status & Monitoring

| RPC | Description |
|---|---|
| `GetStatus` | Overall service state, source statuses, pipeline statuses |
| `GetTableStatus` | Pipelines and indexes for a specific table |
| `GetPipelineStatus` | Single pipeline status with indexes |
| `WatchStatus` | Server-streaming status events (state changes, row changes) |
| `CheckReady` | Readiness check (global, per-source, per-table, per-pipeline) |

#### WatchStatus

Server-streaming RPC that pushes engine events to the client in real time. The stream remains open until the client disconnects.

```protobuf
rpc WatchStatus(WatchStatusRequest) returns (stream WatchStatusResponse);
```

**Request fields:**

| Field | Type | Description |
|---|---|---|
| `tables` | `repeated string` | Filter to events for these tables (format `schema.table`). Empty means all tables. |
| `pipeline_ids` | `repeated string` | Filter to events for these pipeline IDs. Empty means all pipelines. |

Both filters are applied independently. An event matches if it passes both filters (or if the filter is empty). Service-level and source-level events are always delivered regardless of filters.

**Stream event types:**

Each `WatchStatusResponse` contains a `timestamp` and exactly one of the following event variants:

| Event | Fields | Description |
|---|---|---|
| `service_state_change` | `old_state`, `new_state` (`ServiceState` enum) | The overall service state changed (e.g., `BASELINING` to `STREAMING`). |
| `pipeline_state_change` | `pipeline_id`, `old_state`, `new_state` (`PipelineState` enum) | A pipeline transitioned between states (e.g., `INITIALIZING` to `BASELINING`). |
| `source_state_change` | `source_id`, `event_type`, `message` | A source connected or disconnected. `event_type` is `"connected"` or `"disconnected"`. |
| `row_change` | `pipeline_id`, `schema`, `table`, `action`, `position`, `xid` | A change was applied to a target. `action` is `INSERT`, `UPDATE`, `DELETE`, or `TRUNCATE`. |

**Backpressure:** Each watcher has a 256-entry buffer. If the client reads too slowly, events are dropped rather than blocking the engine.

### Source Management

| RPC | Description |
|---|---|
| `GetSourceInfo` | Source details including source-specific metadata |

### Administration

| RPC | Description |
|---|---|
| `ReloadTable` | Trigger re-baseline for a specific table |
| `ReloadAll` | Trigger re-baseline for all tables (optionally on a specific source) |
| `PauseSync` | Pause sync (all or specific source) |
| `ResumeSync` | Resume sync |
| `ResetSource` | Drop and recreate replication slot (and optionally publication) |

### Configuration (read-only)

| RPC | Description |
|---|---|
| `ListTables` | List configured tables with sources and targets |
| `GetTableSchema` | Column definitions for a table |

### Snapshot Management

| RPC | Description |
|---|---|
| `CreateSnapshot` | Trigger snapshot creation |
| `ListSnapshots` | List available snapshots |
| `InspectSnapshot` | Snapshot metadata and table summaries |
| `RestoreSnapshot` | Restore from a snapshot |
| `DeleteSnapshot` | Delete a snapshot |
| `PruneSnapshots` | Delete all but N most recent snapshots |

### Dead Letter Management

| RPC | Description |
|---|---|
| `ListDeadLetters` | List dead letters for a pipeline |
| `ReplayDeadLetters` | Re-deliver dead letters to the target |
| `PurgeDeadLetters` | Remove dead letters |

### Replay

| RPC | Description |
|---|---|
| `StartReplay` | Start replaying a snapshot through a target |
| `GetReplayStatus` | Check replay progress |
| `StopReplay` | Stop an active replay |

## Query Service (`laredo.v1.TableSyncQuery`)

Port: 4001 (same server as OAM)

| RPC | Description |
|---|---|
| `Lookup` | Single-row lookup on a unique index |
| `LookupAll` | Multi-row lookup on a non-unique index |
| `GetRow` | Direct primary key access |
| `ListRows` | Paginated row listing |
| `CountRows` | Row count |
| `Subscribe` | Server-streaming change events with optional replay |

### Subscribe

Server-streaming RPC that pushes change events for a table. Optionally replays all existing rows as `INSERT` events before switching to live changes.

```protobuf
rpc Subscribe(SubscribeRequest) returns (stream SubscribeResponse);
```

**Request fields:**

| Field | Type | Description |
|---|---|---|
| `schema` | `string` | Schema name (required). |
| `table` | `string` | Table name (required). |
| `replay_existing` | `bool` | If `true`, all current rows are sent as `INSERT` events before live streaming begins. |

**Response fields:**

Each `SubscribeResponse` represents a single change event:

| Field | Type | Description |
|---|---|---|
| `action` | `string` | One of `INSERT`, `UPDATE`, `DELETE`, or `TRUNCATE`. |
| `position` | `string` | Source position (e.g., WAL LSN) if available. |
| `xid` | `int64` | Transaction ID if available. |
| `timestamp` | `Timestamp` | When the event was generated. |
| `old_values` | `Struct` | Previous row values (present for `UPDATE` and `DELETE`). |
| `new_values` | `Struct` | New row values (present for `INSERT` and `UPDATE`). |

**Replay behavior:** When `replay_existing` is `true`, the server iterates over all current rows in the indexed target and sends each as an `INSERT` event. The listener is registered before replay begins, so changes that occur during replay are queued and delivered after replay completes. No deduplication is performed -- if a row is modified during replay, the client may see it in both the replay and the live stream.

**Backpressure:** Each subscriber has a 256-entry buffer. If the client consumes too slowly, live events are dropped.

## Replication Service (`laredo.replication.v1.TableSyncReplication`)

Port: 4002 (per fan-out target, configurable)

The Replication service implements the fan-out replication protocol. It allows downstream Laredo instances (or any gRPC client) to replicate table data from an upstream server. Each fan-out target maintains an in-memory copy of the table rows and a change journal, enabling clients to connect, catch up from a known position, and then receive live changes.

| RPC | Type | Description |
|---|---|---|
| `Sync` | server-streaming | Primary replication stream (handshake, snapshot/delta, live) |
| `ListSnapshots` | unary | Available snapshots for client bootstrapping |
| `FetchSnapshot` | server-streaming | Streaming download of a specific snapshot |
| `GetReplicationStatus` | unary | Current sequence, journal bounds, connected clients |

### Sync

The primary server-streaming RPC for replication. A client connects, declares its current state, and receives catch-up data followed by a continuous live stream of changes.

```protobuf
rpc Sync(SyncRequest) returns (stream SyncResponse);
```

**Request fields:**

| Field | Type | Description |
|---|---|---|
| `schema` | `string` | Schema name (required). |
| `table` | `string` | Table name (required). |
| `last_known_sequence` | `int64` | The last sequence number the client has processed. `0` means the client has no state. |
| `last_snapshot_id` | `string` | ID of the last snapshot the client loaded (used for `DELTA_FROM_SNAPSHOT` mode). |
| `client_id` | `string` | Unique identifier for this client. If empty, the server assigns an anonymous ID (`anon-<timestamp>`). |

#### Protocol phases

The stream progresses through three phases:

**1. Handshake**

The server sends a single `SyncHandshake` message that tells the client which sync mode will be used and provides server state:

| Field | Type | Description |
|---|---|---|
| `mode` | `SyncMode` | The sync mode selected by the server (see below). |
| `server_current_sequence` | `int64` | The server's latest sequence number at the time of the handshake. |
| `journal_oldest_sequence` | `int64` | The oldest sequence still available in the server's journal. |
| `resume_from_sequence` | `int64` | The sequence the server will resume sending from. |
| `columns` | `repeated ColumnDefinition` | Table column definitions (schema metadata). |
| `snapshot_id` | `string` | Snapshot ID if the server is sending a snapshot. |

**SyncMode enum:**

| Value | Meaning |
|---|---|
| `SYNC_MODE_FULL_SNAPSHOT` | Client has no state (or its state is too old for delta). Server sends a full snapshot followed by journal catch-up. |
| `SYNC_MODE_DELTA` | Client's `last_known_sequence` is still within the journal window. Server sends only the journal entries since that sequence. |
| `SYNC_MODE_DELTA_FROM_SNAPSHOT` | Client has a snapshot but needs journal entries applied on top of it. |

The server selects the mode automatically:
- If `last_known_sequence > 0` and the sequence is still in the journal (i.e., `>= journal_oldest_sequence`), the server uses `SYNC_MODE_DELTA`.
- Otherwise, the server uses `SYNC_MODE_FULL_SNAPSHOT`.

**2. Snapshot (only for `SYNC_MODE_FULL_SNAPSHOT`)**

If the mode is `SYNC_MODE_FULL_SNAPSHOT`, the server takes a point-in-time snapshot and streams it:

1. `SnapshotBegin` -- contains the `snapshot_id`, the `sequence` at which the snapshot was taken, and the expected `row_count`.
2. One `SnapshotRow` per row -- each contains the row data as a `google.protobuf.Struct`.
3. `SnapshotEnd` -- contains the final `sequence` and the number of `rows_sent`.

After the snapshot, `resume_from_sequence` is set to the snapshot's sequence for the journal catch-up phase.

**3. Journal catch-up and live streaming**

After the handshake (and snapshot if applicable), the server sends all journal entries since `resume_from_sequence` as `ReplicationJournalEntry` messages. Once caught up, the stream transitions to live mode where new changes are sent as they occur.

Each `ReplicationJournalEntry` contains:

| Field | Type | Description |
|---|---|---|
| `sequence` | `int64` | Monotonically increasing sequence number. |
| `source_position` | `string` | Original source position (e.g., PostgreSQL WAL LSN). |
| `timestamp` | `Timestamp` | When the change occurred. |
| `action` | `string` | One of `INSERT`, `UPDATE`, `DELETE`, or `TRUNCATE`. |
| `old_values` | `Struct` | Previous row values (present for `UPDATE` and `DELETE`). |
| `new_values` | `Struct` | New row values (present for `INSERT` and `UPDATE`). |

**Heartbeats:** During idle periods (no changes), the server sends periodic `Heartbeat` messages containing `current_sequence` and `server_time`. The default heartbeat interval is 5 seconds. Clients should treat a gap of 30 seconds without any message as a connection failure and reconnect.

**Schema changes:** If the table schema changes, the server sends a `SchemaChangeNotification` with `old_columns` and `new_columns`.

**Client lifecycle:** The server tracks each connected client's state. During catch-up, the client state is `"catching_up"`. Once all journal entries have been delivered and the client is receiving live changes, the state transitions to `"live"`. If the fan-out target has a `max_clients` limit configured and it has been reached, new `Sync` calls return `RESOURCE_EXHAUSTED`.

#### Stream message types summary

| Message type | Phase | Description |
|---|---|---|
| `SyncHandshake` | Handshake | Always first. Contains mode, server state, and schema. |
| `SnapshotBegin` | Snapshot | Signals start of snapshot data. |
| `SnapshotRow` | Snapshot | One row of snapshot data. |
| `SnapshotEnd` | Snapshot | Signals end of snapshot data. |
| `ReplicationJournalEntry` | Catch-up / Live | A single change event. |
| `SchemaChangeNotification` | Live | Table schema changed. |
| `Heartbeat` | Live | Periodic keepalive during idle periods. |

### ListSnapshots

Returns available snapshots for a fan-out target. Clients can use these to bootstrap by fetching a snapshot via `FetchSnapshot` and then connecting with `Sync` using the snapshot's sequence.

```protobuf
rpc ListSnapshots(ListSnapshotsRequest) returns (ListSnapshotsResponse);
```

**Request fields:**

| Field | Type | Description |
|---|---|---|
| `schema` | `string` | Schema name (required). |
| `table` | `string` | Table name (required). |
| `limit` | `int32` | Maximum number of snapshots to return. `0` means no limit. |

**Response fields:**

The response contains a `repeated ReplicationSnapshotInfo snapshots` array. Each entry:

| Field | Type | Description |
|---|---|---|
| `snapshot_id` | `string` | Unique identifier for this snapshot. |
| `sequence` | `int64` | The journal sequence at the time the snapshot was taken. |
| `source_position` | `string` | Original source position at snapshot time. |
| `row_count` | `int64` | Number of rows in the snapshot. |
| `size_bytes` | `int64` | Approximate size of the snapshot in bytes. |
| `created_at` | `Timestamp` | When the snapshot was created. |

### FetchSnapshot

Streams the contents of a specific snapshot to the client. This is a server-streaming RPC that sends the snapshot in three phases: begin, rows, end.

```protobuf
rpc FetchSnapshot(FetchSnapshotRequest) returns (stream FetchSnapshotResponse);
```

**Request fields:**

| Field | Type | Description |
|---|---|---|
| `snapshot_id` | `string` | ID of the snapshot to fetch (required). Obtained from `ListSnapshots`. |

**Stream format:**

Each `FetchSnapshotResponse` contains exactly one of:

| Chunk type | Fields | Description |
|---|---|---|
| `SnapshotBegin` | `snapshot_id`, `sequence`, `row_count` | First message. Tells the client which snapshot this is, the sequence at which it was taken, and how many rows to expect. |
| `SnapshotRow` | `row` (`Struct`) | One row of data. Sent `row_count` times. |
| `SnapshotEnd` | `sequence`, `rows_sent` | Final message. Confirms the sequence and actual number of rows sent. |

The server searches all fan-out targets across all sources to find the requested snapshot. Returns `NOT_FOUND` if the snapshot ID does not exist.

### GetReplicationStatus

Returns the current state of a fan-out target's replication infrastructure, including the journal, connected clients, and latest snapshot.

```protobuf
rpc GetReplicationStatus(GetReplicationStatusRequest) returns (GetReplicationStatusResponse);
```

**Request fields:**

| Field | Type | Description |
|---|---|---|
| `schema` | `string` | Schema name (required). |
| `table` | `string` | Table name (required). |

**Response fields:**

| Field | Type | Description |
|---|---|---|
| `current_sequence` | `int64` | The latest sequence number in the journal. |
| `journal_oldest_sequence` | `int64` | The oldest sequence still retained in the journal. Clients with a `last_known_sequence` older than this must do a full snapshot sync. |
| `journal_entry_count` | `int64` | Number of entries currently in the journal. |
| `row_count` | `int64` | Total number of rows in the fan-out target's in-memory state. |
| `connected_clients` | `int32` | Number of currently connected `Sync` clients. |
| `clients` | `repeated ConnectedClient` | Per-client details (see below). |
| `latest_snapshot` | `ReplicationSnapshotInfo` | Information about the most recent snapshot, if one exists. |

**ConnectedClient fields:**

| Field | Type | Description |
|---|---|---|
| `client_id` | `string` | The client's identifier (provided in `SyncRequest` or auto-assigned). |
| `current_sequence` | `int64` | The last sequence number delivered to this client. |
| `behind_count` | `int64` | How many journal entries behind the server this client is. |
| `buffer_depth` | `int32` | Number of entries queued in the client's send buffer. |
| `connected_at` | `Timestamp` | When the client connected. |
| `state` | `string` | Client state: `"catching_up"` or `"live"`. |

Returns `NOT_FOUND` if no fan-out target exists for the given schema and table.

## Error Codes

All services use standard gRPC status codes:

| Code | When |
|---|---|
| `INVALID_ARGUMENT` | Missing or malformed required fields. |
| `NOT_FOUND` | Requested resource (table, pipeline, snapshot, fan-out target) does not exist. |
| `FAILED_PRECONDITION` | Operation requires a precondition that is not met (e.g., `confirm=true` for destructive operations, snapshot store not configured). |
| `RESOURCE_EXHAUSTED` | Fan-out target has reached its maximum client limit. |
| `INTERNAL` | Server-side error (e.g., snapshot store I/O failure). |
| `UNIMPLEMENTED` | RPC exists in the proto but is not yet implemented. |
