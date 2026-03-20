---
slug: /
sidebar_position: 1
title: Introduction
---

# Laredo

**Real-time data sync from databases to in-memory targets.** Laredo captures a consistent baseline snapshot of configured tables, then streams all subsequent changes (inserts, updates, deletes, truncates) through pluggable targets. It supports indexed in-memory replicas, HTTP forwarding, replication fan-out, snapshots, and a full gRPC management API.

## What Laredo does

Laredo sits between your database and your application's in-memory state. It handles all the complexity of logical replication, change streaming, ACK coordination, and snapshot management, and exposes clean Go interfaces that any application can use.

```
  PostgreSQL (logical replication)
       |
  ┌────▼─────┐
  │  Laredo   │
  │  Engine   │
  └──┬──┬──┬──┘
     │  │  │
     ▼  ▼  ▼
  Indexed   HTTP    Replication
  Memory    Sync    Fan-Out
  Target    Target  Target
                      |
               ┌──────┼──────┐
               ▼      ▼      ▼
            Client  Client  Client
```

## Key features

- **Consistent baselines** — point-in-time snapshots via PostgreSQL's exported snapshot mechanism, with no gaps between baseline and stream
- **Pluggable targets** — indexed in-memory store, compiled domain objects, HTTP sync, or replication fan-out
- **Pipeline model** — per-table filter and transform chains between source and target
- **ACK coordination** — source positions advance only after all targets confirm durability
- **Snapshot system** — periodic serialization of target state for fast restart without full re-baseline
- **Error isolation** — one pipeline can fail without affecting others
- **Replication fan-out** — multiplex one PostgreSQL slot to N gRPC clients with snapshot + journal + live stream
- **gRPC management** — OAM service for status, reload, pause/resume, snapshot management
- **Query API** — gRPC service for looking up rows in in-memory targets
- **CLI tool** — `laredo` command for inspecting and managing a running server
- **Zero-opinion core** — the library has no config format, transport, or logging dependencies

## Three layers

| Layer | Description | When to use |
|---|---|---|
| **Core Library** | Engine, interfaces, types, pipeline orchestration | Embedding in your Go application |
| **Optional Modules** | gRPC services, HOCON config, metrics, source/target implementations | Adding management or specific data sources |
| **Pre-built Service** | `laredo-server` binary with config, gRPC, metrics, signal handling | Running as a standalone service |

## Next steps

- [Quick Start](/getting-started/quick-start) to get running in under 5 minutes
- [Architecture](/concepts/architecture) to understand the design
- [PostgreSQL Source](/guides/postgresql) for configuring logical replication
- [CLI Reference](/reference/cli) for the `laredo` command
