# target/fanout

Replication fan-out target. Multiplexes one source to N gRPC clients via snapshot + journal + live stream.

## Design Rules

- **Journal is a bounded circular buffer**: entries older than `max_entries` or `max_age` are pruned. Track the oldest retained sequence number.
- **Snapshots are periodic**: serialize in-memory state tagged with journal sequence. Clients behind the journal range must re-snapshot.
- **Client protocol has three phases**: handshake → catch-up (snapshot or delta) → live streaming. All over a single server-streaming gRPC call.
- **Atomic handoff**: pin the journal during snapshot send. After all rows are sent, send journal entries accumulated during transfer, then transition to live. No gaps.
- **Backpressure per client**: configurable buffer size. `drop_disconnect` disconnects slow clients (they'll reconnect and re-snapshot). `slow_down` applies backpressure (risks backing up the journal).
- **Heartbeats**: periodic heartbeat messages on idle connections (default 5s). Clients treat a 30s gap as connection failure.
- **`IsDurable()` always returns `true`**: the in-memory state is authoritative. Journal/snapshot persistence is best-effort for client distribution.

## Testing

- Test journal: append, sequence tracking, pruning by size and age, oldest sequence tracking.
- Test client sync modes: fresh client (FULL_SNAPSHOT), resuming client (DELTA), stale client (FULL_SNAPSHOT).
- Test atomic handoff: changes arriving during snapshot send are included in catch-up.
- Test backpressure: slow client with `drop_disconnect` gets disconnected.
- Test heartbeat: idle connection receives periodic heartbeats.
- Use in-process gRPC for all tests (no network).
