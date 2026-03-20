---
sidebar_position: 3
title: gRPC API
---

# gRPC API Reference

Laredo exposes three gRPC services.

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

## Replication Service (`laredo.replication.v1.TableSyncReplication`)

Port: 4002 (per fan-out target, configurable)

| RPC | Description |
|---|---|
| `Sync` | Primary replication stream (handshake → snapshot/delta → live) |
| `ListSnapshots` | Available snapshots for client bootstrapping |
| `FetchSnapshot` | Streaming download of a specific snapshot |
| `GetReplicationStatus` | Current sequence, journal bounds, connected clients |
