---
sidebar_position: 1
title: Architecture
---

# Architecture

Laredo is organized in three layers, each building on the one below.

## Layer diagram

```
┌─────────────────────────────────────────────────────────┐
│  Pre-built Service ("laredo-server")                    │
│                                                         │
│  - HOCON config loading                                 │
│  - gRPC server (OAM + Query)                            │
│  - Metrics export (Prometheus, OpenTelemetry)            │
│  - Structured logging, signal handling                   │
│  - CLI tool (laredo)                                    │
├─────────────────────────────────────────────────────────┤
│  Optional Modules                                       │
│                                                         │
│  - gRPC OAM/Query service                               │
│  - Config loader (HOCON → engine options)                │
│  - Metrics bridge (observer → metrics backend)           │
├─────────────────────────────────────────────────────────┤
│  Core Library                                           │
│                                                         │
│  - Engine (orchestrator, pipeline management)            │
│  - SyncSource / SyncTarget interfaces                   │
│  - SnapshotStore / SnapshotSerializer                   │
│  - Pipeline filters and transforms                      │
│  - TTL engine, dead letter store                        │
│  - EngineObserver (structured event callbacks)           │
│  - Readiness signaling                                  │
│                                                         │
│  Zero opinions on config, transport, or logging          │
└─────────────────────────────────────────────────────────┘
```

## Core library

The `laredo` Go package defines all public interfaces and types. It has no external dependencies. Everything is pluggable:

- **Sources** produce baseline snapshots and change streams
- **Targets** consume rows and changes
- **Filters/transforms** sit in the pipeline between source and target
- **Snapshot stores** persist target state for fast restart
- **Observers** receive structured events (no logging in core)

## Engine

The engine is the orchestrator. It manages a set of **pipelines**, each binding a source table to a target. The engine handles:

- Startup path selection (cold start, resume, or snapshot restore)
- Baseline loading and change streaming
- ACK coordination across targets sharing a source
- Backpressure and buffering
- Error isolation per pipeline
- TTL/expiry scanning
- Snapshot scheduling

## Package layout

```
laredo.go, types.go, source.go, ...   Core interfaces (root package)
internal/engine/                       Engine implementation
source/{pg,kinesis,testsource}/        Source implementations
target/{httpsync,memory,fanout}/       Target implementations
snapshot/{local,s3,jsonl}/             Snapshot implementations
filter/, transform/                    Built-in pipeline components
service/{oam,query,replication}/       gRPC services
config/                                HOCON config loading
metrics/{prometheus,otel}/             Metrics bridges
client/fanout/                         Go fan-out client library
cmd/laredo-server/                     Pre-built service binary
cmd/laredo/                            CLI tool
```
