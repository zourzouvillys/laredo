# test/testutil

Shared test infrastructure for all laredo packages. This package is imported by tests across the module.

## Rules

- **No mocks.** Provide real, programmable implementations instead.
- **Helpers return values, not pointers** (unless the type requires it). Keep the API simple.
- **Factories are deterministic**: `SampleTable()`, `SampleColumns()`, `SampleRow(id, name)` always return the same shape. Tests that need variation should build on top of these.
- **TestObserver records everything**: all `EngineObserver` callbacks append to a thread-safe event list. Use `EventsByType("ChangeApplied")` to assert on specific events.
- **Assertions go in the test, not in helpers**: helpers set up state and return it. The test does the asserting.

## What belongs here

- `TestObserver` — recording stub for `EngineObserver` (exists)
- Sample data factories — `SampleTable()`, `SampleColumns()`, `SampleRow()` (exists)
- `NewTestEngine(t, ...Option)` — creates an engine with testsource + indexed memory for quick pipeline tests
- `NewTestPostgres(t)` — starts a PostgreSQL testcontainer, returns connection string (for integration tests)
- `NewTestGRPCServer(t, engine)` — starts an in-process gRPC server, returns client connection
- `AssertEventually(t, timeout, condition)` — poll-based assertion for async operations
- `CollectRows(target)` — extract all rows from an IndexedTarget for assertion

## What does NOT belong here

- Package-specific test helpers (put those in the package's own `_test.go` files)
- Test fixtures or data files (embed them in the test that uses them)
- Mocks of any kind
