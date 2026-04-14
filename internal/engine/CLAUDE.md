# internal/engine

Internal engine sub-components. The main engine orchestrator (`coreEngine`) lives in
the root `laredo` package to avoid import cycles with the public API types. This package
holds sub-components that the engine delegates to.

## Design Rules

- **Single-goroutine pipeline dispatch**: each pipeline runs in its own goroutine. The engine goroutine coordinates startup, shutdown, and cross-pipeline operations (ACK, snapshots).
- **No locks in the hot path**: use channels for communication between goroutines. Mutexes only for read-heavy shared state (e.g., readiness).
- **ACK is minimum-confirmed**: the ACK tracker computes the minimum durable position across all pipelines sharing a source. Only that minimum is ACKed to the source.
- **Error isolation is the default**: a failing pipeline transitions to ERROR state independently. Other pipelines continue.
- **Buffer policies are per-pipeline**: each pipeline has its own bounded channel. The policy (block/drop_oldest/error) is enforced at the buffer, not the dispatcher.

## Testing

- Unit test each component in isolation: buffer, ACK tracker, TTL manager, readiness tracker.
- Use `source/testsource` for engine-level tests — never mock `SyncSource`.
- Use `target/memory.NewIndexedTarget` as the default target in engine tests — it's the simplest real target.
- Test state machine transitions: verify pipeline states move correctly through INITIALIZING → BASELINING → STREAMING → PAUSED/ERROR/STOPPED.
- Test ACK coordination with multiple pipelines sharing a source: verify minimum position tracking.
- Test error isolation: one pipeline fails, others continue, ACK advances past the failed pipeline.

## Files

| File | Owns |
|---|---|
| `engine.go` | Package doc only; main engine struct is in root `laredo` package |
| `buffer.go` | Bounded change buffer with block/drop_oldest/error policies |
| `ack.go` | ACK coordinator: minimum confirmed position across pipelines |
| `readiness.go` | Readiness tracker: per-pipeline, per-source, global |

Pipeline dispatch, TTL scanning, and graceful shutdown are currently implemented inside the root `laredo` package's engine, not as separate sub-components here.
