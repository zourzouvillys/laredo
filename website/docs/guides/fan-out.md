---
sidebar_position: 4
title: Replication Fan-Out
---

# Replication Fan-Out

Multiplex one PostgreSQL replication slot to N downstream clients over gRPC. Clients connect, receive a consistent snapshot, then stream live changes — no need for each service instance to hold its own slot.

## Architecture

```
PostgreSQL (1 slot)
       │
  ┌────▼─────┐
  │  Engine   │
  └────┬──────┘
       │
  ┌────▼──────────────┐
  │  Fan-Out Target   │
  │                   │
  │  In-Memory State  │
  │  Change Journal   │
  │  Periodic Snaps   │
  │  gRPC Server      │
  └───┬───┬───┬───────┘
      │   │   │
      ▼   ▼   ▼
    Client A  B  C
```

## Server configuration

```hocon
targets = [{
  type = replication-fanout

  journal {
    max_entries = 1000000
    max_age = 24h
  }

  snapshot {
    interval = 5m
    store = local
    store_config { path = "/var/lib/laredo/fanout-snapshots" }
    serializer = jsonl
    retention { keep_count = 5, max_age = 1h }
  }

  grpc {
    port = 4002
    max_clients = 500
  }

  client_buffer {
    max_size = 50000
    policy = drop_disconnect
  }
}]
```

## Client protocol

The client connects via a single server-streaming gRPC call:

1. **Handshake** — client declares its state (fresh or has a local snapshot)
2. **Catch-up** — server sends full snapshot or journal delta
3. **Live streaming** — changes in real time

```
Fresh client:       → FULL_SNAPSHOT → journal catch-up → live
Resuming client:    → DELTA (journal entries from last sequence) → live
Stale client:       → FULL_SNAPSHOT (too far behind journal) → live
```

## Go client

```go
client, err := fanout.New(
    fanout.ServerAddress("laredo-server:4002"),
    fanout.Table("public", "config_document"),
    fanout.LocalSnapshotPath("/var/lib/myapp/laredo-cache"),
    fanout.WithIndexedState(
        memory.LookupFields("instance_id", "key"),
        memory.AddIndex("by_instance", []string{"instance_id"}, false),
    ),
    fanout.ClientID("myapp-instance-abc123"),
)

client.Start()
client.AwaitReady(30 * time.Second)

row, ok := client.Lookup("inst_abc", "rulesets/default")

client.Listen(func(old, new laredo.Row) {
    // react to changes
})

client.Stop() // saves local snapshot for fast restart
```

## Consistency guarantees

- **Snapshot + journal = complete state**: no gaps, no duplicates
- **Strict ordering**: journal entries delivered in sequence order
- **Atomic handoff**: journal is pinned during snapshot send — no gap between snapshot and stream
- **At-least-once on reconnect**: clients must be idempotent for the entry at their declared sequence
