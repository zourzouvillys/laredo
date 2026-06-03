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
Fresh client:        → FULL_SNAPSHOT → journal catch-up → live
Resuming client:     → DELTA (journal entries from last sequence) → live
Failing-over client: → DELTA (journal entries from last source position) → live
Stale client:        → FULL_SNAPSHOT (too far behind journal) → live
```

When the server is draining it sends a `GoAway` message; the client re-dials and
resumes on another instance by source position. See
[Failover & zero-downtime deploys](#failover--zero-downtime-deploys).

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

## Failover & zero-downtime deploys

A client can hand off from one `laredo-server` instance to another — during a
rolling deploy or when an instance is being replaced — and **resume without a
full re-sync**.

### Why it works: resume by source position

Each fan-out instance assigns its own journal **sequence** numbers, so a
sequence is meaningless on a different instance. The coordinate that *is* stable
across instances is the source position — the PostgreSQL **WAL LSN**. Every
journal entry carries its `source_position`, and the client persists the LSN of
the last change it applied. On failover it resumes from that LSN.

This requires that both instances tail the **same** PostgreSQL (each with its
own replication slot, both reading the same publication). The same row change
then yields the same LSN on every instance, so any instance can answer "give me
everything after LSN X".

```
Instance A (slot_a) ─┐
                     ├─ same publication ⇒ same LSN per change
Instance B (slot_b) ─┘
```

### The handoff

1. The draining instance sends a **`GoAway`** control message on each client's
   stream (it keeps the stream open so the client can overlap the cutover).
2. The client re-dials its **configured address** — a load balancer or headless
   DNS routes the new connection to a healthy instance — and resumes with
   `last_known_source_position` set to its last applied LSN.
3. The new instance replies with a **`DELTA`** (no snapshot) containing every
   change after that LSN, then goes live.
4. Once the new stream has caught up, the client closes the old stream cleanly,
   letting the draining instance finish shutting down.

If the new instance has already pruned the journal back past the client's LSN,
it falls back to a full snapshot automatically — correctness is preserved, only
the optimization is lost.

Resume is **at-least-once**: during the brief overlap (and when an instance is
momentarily behind), the client may re-apply changes it already has. Targets are
keyed by primary key and apply idempotently, so this is safe.

### Triggering a drain

**On shutdown (rolling deploys).** Start `laredo-server` with `--drain-grace`:

```bash
laredo-server --config laredo.conf --drain-grace 15s
```

On `SIGTERM` the server marks `/health/ready` unready (so the load balancer
deregisters it), tells fan-out clients to hand off, waits up to the grace period
for them to leave, then stops.

**On demand (operator).** Use the OAM admin RPC to drain a running instance —
e.g. to cordon it before maintenance:

```
LaredoOAMService.DrainReplication { schema: "public", table: "config_document" }
```

Omit `schema`/`table` to drain every fan-out target. `drain_deadline_seconds`
optionally caps how long the server keeps serving drained streams.

### Operations

- Put fan-out instances behind an L4/L7 load balancer or headless service and
  point clients at that address — not at individual pods.
- Size `journal.max_entries` / `journal.max_age` for your **slowest** consumer
  and your deploy cadence: an instance must retain entries back to the LSN of any
  client that might fail over to it, or that client re-snapshots.
- Set `--drain-grace` comfortably above your LB's deregistration delay plus a
  typical client catch-up.

### Troubleshooting

- **Clients full-resync on every deploy** — the new instance's journal doesn't
  reach back to their LSN. Increase `journal.max_entries`/`max_age`, or shorten
  the deploy gap.
- **New connections land on the draining instance** — the LB hasn't
  deregistered it yet. Ensure it scrapes `/health/ready` and increase
  `--drain-grace`.
- **`GoAway` never arrives** — the fan-out target isn't being drained. Confirm
  `--drain-grace > 0` (SIGTERM path) or that `DrainReplication` matched a target.

## Consistency guarantees

- **Snapshot + journal = complete state**: no gaps, no duplicates
- **Strict ordering**: journal entries delivered in sequence order
- **Atomic handoff**: journal is pinned during snapshot send — no gap between snapshot and stream
- **Portable resume**: clients resume across instances by source position (WAL LSN), not per-instance sequence
- **At-least-once on reconnect/failover**: clients must be idempotent (they apply by primary key)
- **At-least-once on reconnect**: clients must be idempotent for the entry at their declared sequence
