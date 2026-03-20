# target/memory

Compiled and indexed in-memory SyncTarget implementations.

## Design Rules

- **Thread safety**: `sync.RWMutex` protects all data structures. The engine writes from a single goroutine; external reads (query API, listeners) take read locks.
- **Listeners are synchronous**: listener callbacks run under the write lock. They must not block.
- **IndexedTarget**: schema-agnostic, stores raw `Row` maps. Primary lookup index (unique, from `lookup_fields`) + configurable secondary indexes (unique or non-unique).
- **CompiledTarget**: typed domain objects via pluggable compiler function. Key extraction from configured fields. Optional filter predicate.
- **Both targets**: `IsDurable()` always returns `true` (memory is the store). Default `OnSchemaChange` response is `RE_BASELINE`.

## Testing

- Test insert/update/delete/truncate for both target types.
- Test index maintenance: insert adds to all indexes, update removes old + inserts new, delete removes from all.
- Test concurrent reads during writes (use `sync.WaitGroup` with reader goroutines).
- Test listener notifications: verify (old, new) pairs for each operation.
- Test compiled target's filter: row passes filter → stored; row fails filter → skipped; update causes filter failure → treated as delete.
- Test `ExportSnapshot` / `RestoreSnapshot` round-trip.
