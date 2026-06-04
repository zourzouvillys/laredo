---
sidebar_position: 1
title: Architecture
---

# Architecture

Laredo is organized in three layers, each building on the one below.

<iframe src="/laredo/viz/pipeline.html?embed=1" title="The Laredo pipeline" loading="lazy" class="embed"></iframe>

## Layer diagram

<svg class="diagram" viewBox="0 0 720 326" role="img" aria-label="Three layers: pre-built service on top, optional modules in the middle, core library at the base.">
  <!-- base: core library -->
  <rect x="6" y="170" width="708" height="150" rx="12" fill="var(--bg-subtle)" stroke="var(--border)" stroke-width="1.5"/>
  <text x="28" y="196" fill="var(--fg)" font-size="15" font-weight="700">Core library</text>
  <text x="690" y="194" text-anchor="end" fill="var(--fg-faint)" font-size="10">github.com/zourzouvillys/laredo</text>
  <text x="28" y="222" fill="var(--fg-secondary)" font-size="11.5">Engine (orchestrator) · SyncSource / SyncTarget · SnapshotStore / Serializer</text>
  <text x="28" y="242" fill="var(--fg-secondary)" font-size="11.5">pipeline filters &amp; transforms · TTL · dead-letter store</text>
  <text x="28" y="262" fill="var(--fg-secondary)" font-size="11.5">EngineObserver (structured events) · readiness signaling</text>
  <text x="28" y="296" fill="var(--accent)" font-size="11.5" font-style="italic">Zero opinions on config, transport, or logging.</text>

  <!-- middle: optional modules -->
  <rect x="6" y="92" width="708" height="66" rx="12" fill="var(--bg-muted)" stroke="var(--border)" stroke-width="1.5"/>
  <text x="28" y="118" fill="var(--fg)" font-size="15" font-weight="700">Optional modules</text>
  <text x="690" y="116" text-anchor="end" fill="var(--fg-faint)" font-size="10">opt-in</text>
  <text x="28" y="142" fill="var(--fg-secondary)" font-size="11.5">gRPC services (OAM · Query · Replication) · HOCON config loader · metrics bridges</text>

  <!-- top: pre-built service -->
  <rect x="6" y="6" width="708" height="74" rx="12" fill="var(--bg)" stroke="var(--accent)" stroke-width="2"/>
  <text x="28" y="32" fill="var(--accent)" font-size="15" font-weight="700">Pre-built service — laredo-server</text>
  <text x="690" y="30" text-anchor="end" fill="var(--fg-faint)" font-size="10">a binary</text>
  <text x="28" y="56" fill="var(--fg-secondary)" font-size="11.5">HOCON config · gRPC server · metrics export · structured logging · signals · CLI</text>
</svg>

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
