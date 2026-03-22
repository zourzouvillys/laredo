---
sidebar_position: 8
title: Scaling Fan-Out
---

# Scaling Fan-Out Clients

The replication fan-out target multiplexes one source to N gRPC clients. Each client maintains a local in-memory replica of the table. As you add more clients, you need to tune the fan-out target's configuration for journal sizing, snapshot frequency, client limits, and resource usage.

## Architecture overview

The fan-out target maintains:

- **In-memory row store**: the current state of the table
- **Change journal**: a bounded circular buffer of recent changes (inserts, updates, deletes)
- **Snapshots**: periodic point-in-time captures for bootstrapping new clients
- **Client registry**: tracks connected clients, their positions, and buffer state

When a new client connects, the fan-out target either sends a full snapshot (if the client is new or too far behind) or a delta from the journal (if the client's last known sequence is within journal range). After catch-up, the client transitions to live streaming.

## Configuring max_clients

The `max_clients` setting limits the number of concurrent gRPC connections to the fan-out target. When the limit is reached, new connections are rejected.

```hocon
pipelines = [
  {
    source = pg_main
    table = public.config_document
    target {
      type = replication-fanout
      grpc { port = 4002, max_clients = 500 }
    }
  }
]
```

Set `max_clients` based on expected fleet size plus headroom for rolling deployments (when both old and new instances are connected). A value of `0` means unlimited (no enforcement).

Each connected client consumes:

- One gRPC server-stream goroutine
- A per-client send buffer (configurable via `client_buffer.max_size`, default 50,000 entries)
- Memory for buffered but unsent changes

### Monitoring connected clients

```bash
laredo fanout clients public.config_document
```

Output:

```
CLIENT ID               CONNECTED           STATE         SEQUENCE    BUFFER
svc-orders-a1b2c3       2026-03-20 14:00    live          1847293     0
svc-orders-d4e5f6       2026-03-20 14:01    live          1847293     0
svc-billing-g7h8i9      2026-03-20 13:55    catching_up   1847100     2341
```

Via Prometheus:

```
laredo_fanout_connected_clients{table="public.config_document"}
```

Alert when approaching the limit:

```yaml
- alert: FanoutClientsNearLimit
  expr: laredo_fanout_connected_clients / laredo_fanout_max_clients > 0.9
  for: 5m
  annotations:
    summary: "Fan-out client count approaching max_clients limit"
```

## Journal sizing

The journal determines how far back a reconnecting client can catch up without a full snapshot. If a client's last known sequence is older than the oldest journal entry, it must receive a full snapshot instead of a delta.

```hocon
target {
  type = replication-fanout
  journal {
    max_entries = 1000000
    max_age = 24h
  }
}
```

### Sizing for your workload

| Factor | Consideration |
|---|---|
| Change rate | High change rates fill the journal faster; increase `max_entries` |
| Client restart time | If clients take 5 minutes to restart, ensure the journal covers at least 5 minutes of changes |
| Rolling deployments | During a rolling deploy, all instances restart. Journal must cover the full deployment window |
| Memory cost | Each journal entry stores the old and new row values; size accordingly |

To check the current journal state:

```bash
laredo fanout status public.config_document
```

Output includes:

```
Journal:
  Current sequence:    1847293
  Oldest sequence:     1842000
  Entry count:         5293
```

If many clients are falling back to full snapshots (visible in the `catching_up` state and log messages), increase `max_entries` or `max_age`.

### Memory impact

Each journal entry holds a copy of the old and new row values. For a table with 1 KB average row size and 1,000,000 max entries, the journal alone uses roughly 2 GB of memory (old + new values). Size `max_entries` based on available memory and change velocity.

## Snapshot configuration for fan-out

Fan-out snapshots are separate from the engine-level snapshot store. They are held in memory and used to bootstrap clients who are too far behind the journal.

```hocon
target {
  type = replication-fanout
  snapshot {
    interval = 5m
    retention {
      keep_count = 5
    }
  }
}
```

| Option | Default | Description |
|---|---|---|
| `interval` | disabled | How often to take a fan-out snapshot |
| `retention.keep_count` | unlimited | Maximum number of snapshots to retain |
| `retention.max_age` | unlimited | Maximum age of retained snapshots |

When many clients connect at once (e.g., during a fleet-wide restart), they all need a snapshot. Having recent snapshots ready avoids serializing the full in-memory state for each new client.

To list fan-out snapshots:

```bash
laredo fanout snapshots public.config_document
```

## LocalSnapshotPath for fast client restarts

The fan-out client (`client/fanout`) supports saving its in-memory state to disk on shutdown and restoring it on startup. This avoids requesting a full snapshot from the server on every restart.

```go
client := fanout.New(
    fanout.ServerAddress("localhost:4002"),
    fanout.Table("public", "config_document"),
    fanout.ClientID("svc-orders-a1b2c3"),
    fanout.LocalSnapshotPath("/var/lib/myapp/fanout-state.json"),
)
```

When `LocalSnapshotPath` is configured:

1. **On startup**: if the file exists, the client loads it and sends its `last_known_sequence` to the server. If the sequence is within journal range, the server sends a delta. If not, a full snapshot.
2. **On stop**: the client serializes its current row store and last sequence number to disk.

### Benefits at scale

- **Faster restarts**: clients resume from local state + delta instead of full snapshot
- **Less server load**: fewer full snapshot transfers during rolling deployments
- **Lower network usage**: deltas are much smaller than full snapshots

### Recommendations

- Use a persistent volume (not ephemeral container storage) for the snapshot file
- In Kubernetes, use a PersistentVolumeClaim or a hostPath volume
- The file is JSON, typically sized proportional to the row count (table size in memory)
- If the file is missing or corrupted, the client falls back to a full snapshot from the server

## Client buffer and backpressure

Each connected client has a send buffer. When a client is slow to consume changes, the buffer fills up.

```hocon
target {
  type = replication-fanout
  client_buffer {
    max_size = 50000
    policy = drop_disconnect
  }
}
```

| Policy | Behavior |
|---|---|
| `drop_disconnect` (default) | Disconnects the slow client. It reconnects and re-syncs from a snapshot or journal delta. |
| `slow_down` | Applies backpressure from the slow client back into the fan-out target. Risks slowing down all clients. |

For most deployments, `drop_disconnect` is the safer choice. A disconnected client reconnects automatically and catches up from its last known position.

Monitor buffer depth:

```bash
laredo fanout clients public.config_document
# The BUFFER column shows queued entries per client
```

## When to add another fan-out target

A single fan-out target has limits:

- **Memory**: the in-memory row store, journal, and snapshots all consume memory
- **CPU**: serializing snapshots for many concurrent new clients can spike CPU
- **Network**: many clients streaming from one gRPC port can saturate bandwidth

When you hit these limits, consider adding a second fan-out target on a different laredo-server instance. The second instance connects to the same PostgreSQL source (or uses the fan-out client itself as a cascading replication layer).

### Cascading fan-out

Use the fan-out client as a source for a second laredo-server:

```
PostgreSQL → laredo-server-1 (fan-out, 500 clients)
                    ↓
             laredo-server-2 (fan-out, 500 clients, sourced from server-1)
```

This spreads the client load across multiple servers while keeping only one replication slot on PostgreSQL.

### Splitting by table

If different services consume different tables, assign each table to its own fan-out target on a separate port:

```hocon
pipelines = [
  {
    source = pg_main
    table = public.config_document
    target {
      type = replication-fanout
      grpc { port = 4002, max_clients = 500 }
    }
  },
  {
    source = pg_main
    table = public.user_preferences
    target {
      type = replication-fanout
      grpc { port = 4003, max_clients = 200 }
    }
  }
]
```

This isolates resource usage per table and allows independent scaling.

## Capacity planning checklist

- **Memory**: row store size + (journal `max_entries` x average row size x 2) + (snapshot count x row store size)
- **Connections**: `max_clients` + headroom for rolling deployments (typically 2x normal count during deploy)
- **Journal depth**: covers at least the longest expected client downtime (restart, deploy, network partition)
- **Snapshot frequency**: frequent enough that new clients get a recent snapshot without waiting
- **Client buffer size**: large enough to absorb short bursts; small enough to disconnect genuinely slow clients promptly
- **LocalSnapshotPath**: configured on all clients to minimize full snapshot requests
