---
sidebar_position: 4
title: Replication Fan-Out
---

# Replication Fan-Out

Multiplex one PostgreSQL replication slot to N downstream clients over gRPC. Clients connect, receive a consistent snapshot, then stream live changes — no need for each service instance to hold its own slot.

<iframe src="/laredo/viz/fan-out.html?embed=1" title="Replication fan-out" loading="lazy" class="embed"></iframe>

## Architecture

<svg class="diagram" viewBox="0 0 720 300" role="img" aria-label="One PostgreSQL slot feeds the engine and a fan-out target (in-memory state, change journal, periodic snapshots, gRPC server) which serves clients A, B and C.">
  <defs>
    <marker id="fa-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 L10 5 L0 10 z" fill="var(--fg-muted)"/></marker>
  </defs>
  <!-- postgres -->
  <rect x="278" y="10" width="164" height="40" rx="9" fill="var(--bg-subtle)" stroke="var(--allow)" stroke-width="1.5"/>
  <text x="360" y="35" text-anchor="middle" fill="var(--allow)" font-size="12" font-weight="700">PostgreSQL · 1 slot</text>
  <line x1="360" y1="50" x2="360" y2="70" stroke="var(--fg-muted)" stroke-width="1.6" marker-end="url(#fa-arrow)"/>
  <!-- engine -->
  <rect x="288" y="72" width="144" height="38" rx="9" fill="var(--bg-subtle)" stroke="var(--border)" stroke-width="1.5"/>
  <text x="360" y="96" text-anchor="middle" fill="var(--fg-secondary)" font-size="12" font-weight="700">Engine</text>
  <line x1="360" y1="110" x2="360" y2="130" stroke="var(--fg-muted)" stroke-width="1.6" marker-end="url(#fa-arrow)"/>
  <!-- fan-out target -->
  <rect x="232" y="132" width="256" height="96" rx="11" fill="var(--bg)" stroke="var(--accent)" stroke-width="2"/>
  <text x="360" y="153" text-anchor="middle" fill="var(--accent)" font-size="12.5" font-weight="700">Fan-Out Target</text>
  <text x="360" y="174" text-anchor="middle" fill="var(--fg-secondary)" font-size="11">in-memory state · change journal</text>
  <text x="360" y="192" text-anchor="middle" fill="var(--fg-secondary)" font-size="11">periodic snapshots · gRPC server</text>
  <!-- clients -->
  <g stroke="var(--fg-muted)" stroke-width="1.6" fill="none" marker-end="url(#fa-arrow)">
    <path d="M300 228 C 300 250, 180 248, 150 264"/>
    <path d="M360 228 L 360 264"/>
    <path d="M420 228 C 420 250, 540 248, 570 264"/>
  </g>
  <g font-size="11.5" font-weight="700">
    <rect x="96" y="266" width="108" height="30" rx="8" fill="var(--canary-soft)" stroke="var(--canary)" stroke-width="1.2"/><text x="150" y="285" text-anchor="middle" fill="var(--canary)">client A</text>
    <rect x="306" y="266" width="108" height="30" rx="8" fill="var(--canary-soft)" stroke="var(--canary)" stroke-width="1.2"/><text x="360" y="285" text-anchor="middle" fill="var(--canary)">client B</text>
    <rect x="516" y="266" width="108" height="30" rx="8" fill="var(--canary-soft)" stroke="var(--canary)" stroke-width="1.2"/><text x="570" y="285" text-anchor="middle" fill="var(--canary)">client C</text>
  </g>
</svg>

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

## Subscription filtering

A client can ask for **only the rows it cares about** by attaching one or more
column predicates to its `Sync` request. The server applies them uniformly
across the snapshot, journal catch-up, and live phases, so the subscriber gets a
consistent slice of the table — and **rows it filters out never cross the wire**.

The common use is **partition scoping**: a single equality predicate on a
partition column gives each subscriber its own slice of a shared table without a
separate pipeline. This is the right tool for multi-tenant fan-out — one fan-out
target for an `events` table, each tenant subscribing with `tenant_id` equal to
its own id — with hard isolation, because other tenants' rows are never sent.

### From the Go client

```go
client := fanout.New(
    fanout.ServerAddress("laredo-server:4002"),
    fanout.Table("public", "events"),
    fanout.WithFilterEquals("tenant_id", "acme"), // partition scope
    // ...AND-combined with any further predicates:
    // fanout.WithFilterPrefix("key", "rulesets/"),
    // fanout.WithFilterIn("region", "eu", "us"),
)
```

The client's local replica then contains only matching rows; `Count()`, `Get`,
`Lookup`, `All`, `Listen`, and any secondary indexes operate over the filtered
slice.

### Predicate types

| Client option / wire field | Matches |
|---|---|
| `WithFilterEquals(field, v)` / `equals` | the column equals `v`. Numbers compare numerically (a predicate of `42` matches an integer or float 42); strings and bools by value. |
| `WithFilterPrefix(field, p)` / `prefix` | the (string) column starts with `p`. |
| `WithFilterIn(field, v...)` / `in` | the column equals one of the values. |

Multiple predicates are **AND-combined**. A predicate is evaluated against the
post-change row for inserts and updates, and the pre-change row for deletes;
`TRUNCATE` always passes (it is structural). A missing or null column never
matches.

### Caveats

- **Filter columns must be stable for a row's lifetime.** Subscription filtering
  assumes a row never changes the partition it belongs to. If an update moves a
  row from one partition value to another, a subscriber on the old value is not
  told the row left, and a subscriber on the new value sees it as an update. For
  partition keys like `tenant_id` this never happens; design filter columns to be
  immutable.
- **Filtered resume.** A filtered client resumes from the last sequence it
  *received*. If no matching change occurs for a long time while the journal
  prunes past that point, a reconnect falls back to a (filtered) full snapshot —
  correct, just without the delta optimization. Size `journal.max_entries` /
  `journal.max_age` with your sparsest partition in mind, the same way you size
  for your slowest consumer.
- **Filtering is about delivery, not memory.** The fan-out target still holds the
  whole table in memory; filtering controls only what is sent to each subscriber
  (and the isolation between them).

## Failover & zero-downtime deploys

A client can hand off from one `laredo-server` instance to another — during a
rolling deploy or when an instance is being replaced — and **resume without a
full re-sync**.

<iframe src="/laredo/viz/failover.html?embed=1" title="Cross-instance failover" loading="lazy" class="embed"></iframe>

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

<svg class="diagram" viewBox="0 0 720 130" role="img" aria-label="Instance A (slot_a) and Instance B (slot_b) read the same publication, so the same change yields the same LSN on both." style="max-width:560px">
  <rect x="20" y="20" width="180" height="34" rx="8" fill="var(--bg-subtle)" stroke="var(--accent)" stroke-width="1.5"/>
  <text x="110" y="42" text-anchor="middle" fill="var(--accent)" font-size="12" font-weight="700">Instance A · slot_a</text>
  <rect x="20" y="76" width="180" height="34" rx="8" fill="var(--bg-subtle)" stroke="var(--accent)" stroke-width="1.5"/>
  <text x="110" y="98" text-anchor="middle" fill="var(--accent)" font-size="12" font-weight="700">Instance B · slot_b</text>
  <path d="M200 37 C 250 37, 250 65, 290 65" stroke="var(--fg-muted)" stroke-width="1.6" fill="none"/>
  <path d="M200 93 C 250 93, 250 65, 290 65" stroke="var(--fg-muted)" stroke-width="1.6" fill="none"/>
  <rect x="290" y="48" width="200" height="34" rx="8" fill="var(--allow-soft)" stroke="var(--allow)" stroke-width="1.5"/>
  <text x="390" y="70" text-anchor="middle" fill="var(--allow)" font-size="11.5" font-weight="700">same publication</text>
  <text x="510" y="69" fill="var(--fg-secondary)" font-size="12">⇒ same LSN per change</text>
</svg>

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
