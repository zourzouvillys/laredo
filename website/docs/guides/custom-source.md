---
sidebar_position: 10
title: Custom Source Implementation
---

# Custom Source Implementation

Laredo sources are pluggable. If the built-in sources (PostgreSQL, S3+Kinesis) do not cover your data source, you can implement the `SyncSource` interface and wire it into the engine.

This guide walks through the interface, the lifecycle the engine expects, and a complete minimal example.

## The SyncSource interface

```go
type SyncSource interface {
    Init(ctx context.Context, config SourceConfig) (map[TableIdentifier][]ColumnDefinition, error)
    ValidateTables(ctx context.Context, tables []TableIdentifier) []ValidationError
    Baseline(ctx context.Context, tables []TableIdentifier, rowCallback func(TableIdentifier, Row)) (Position, error)
    Stream(ctx context.Context, from Position, handler ChangeHandler) error
    Ack(ctx context.Context, position Position) error
    SupportsResume() bool
    LastAckedPosition(ctx context.Context) (Position, error)
    ComparePositions(a, b Position) int
    PositionToString(p Position) string
    PositionFromString(s string) (Position, error)
    Pause(ctx context.Context) error
    Resume(ctx context.Context) error
    GetLag() LagInfo
    OrderingGuarantee() OrderingGuarantee
    State() SourceState
    Close(ctx context.Context) error
}
```

Each method is described below in the order the engine calls them.

## Lifecycle

The engine drives sources through a fixed lifecycle:

```
Init -> ValidateTables -> Baseline -> Stream (blocking)
                                        |
                                        +--> Ack (called periodically)
                                        |
                                        +--> Pause / Resume (on demand)
                                        |
                                        v
                                      Close
```

### Init

```go
Init(ctx context.Context, config SourceConfig) (map[TableIdentifier][]ColumnDefinition, error)
```

Called once at startup. The engine passes a `SourceConfig` containing the list of `TableIdentifier` values this source is responsible for. Your implementation should:

1. Establish a connection to the data source.
2. Discover column schemas for each requested table.
3. Return a map from table to its column definitions.

If the connection fails or a table does not exist, return an error. The engine will not proceed.

### ValidateTables

```go
ValidateTables(ctx context.Context, tables []TableIdentifier) []ValidationError
```

Called after `Init`. Check that every requested table exists and is accessible. Return a `ValidationError` for each table that fails validation. Return an empty (or nil) slice if everything is fine.

### Baseline

```go
Baseline(ctx context.Context, tables []TableIdentifier, rowCallback func(TableIdentifier, Row)) (Position, error)
```

Produce a consistent point-in-time snapshot. For each row in each table, call `rowCallback(table, row)`. When done, return a `Position` that marks the end of the snapshot. The engine will later pass this position to `Stream` so that streaming begins exactly where the baseline left off.

Key rules:

- The snapshot must be consistent. If your data source supports transactions, use a snapshot-isolation or repeatable-read transaction.
- Call `rowCallback` for every row. Do not skip rows or return partial results.
- The position you return must be usable as a "from" argument to `Stream`.

### Stream

```go
Stream(ctx context.Context, from Position, handler ChangeHandler) error
```

Begin streaming changes starting after `from`. This method blocks until the context is cancelled or an unrecoverable error occurs. For each change, call `handler.OnChange(event)` with a `ChangeEvent`:

```go
type ChangeEvent struct {
    Table     TableIdentifier
    Action    ChangeAction  // ActionInsert, ActionUpdate, ActionDelete, ActionTruncate
    Position  Position
    Timestamp time.Time
    NewValues Row           // set for insert and update
    OldValues Row           // set for update and delete (if available)
}
```

Return `ctx.Err()` when the context is cancelled (normal shutdown). Return any other error for failures.

### Ack

```go
Ack(ctx context.Context, position Position) error
```

The engine calls `Ack` after all targets on this source have durably processed changes up to `position`. For stateful sources, persist this position so you can resume from it later. For ephemeral sources, this can be a no-op.

### SupportsResume and LastAckedPosition

```go
SupportsResume() bool
LastAckedPosition(ctx context.Context) (Position, error)
```

If your source can resume from a previously ACKed position (skipping the baseline on restart), return `true` from `SupportsResume` and the last saved position from `LastAckedPosition`.

If not, return `false` and `nil`. The engine will run a full baseline on every startup.

### Position methods

```go
ComparePositions(a, b Position) int
PositionToString(p Position) string
PositionFromString(s string) (Position, error)
```

The engine needs to compare, serialize, and deserialize positions. `Position` is `any` -- your source defines the concrete type (a `uint64`, a custom struct, a string).

- `ComparePositions`: return negative if `a < b`, zero if equal, positive if `a > b`.
- `PositionToString`: serialize for storage, display, and gRPC transport.
- `PositionFromString`: deserialize from a string previously produced by `PositionToString`.

### Pause and Resume

```go
Pause(ctx context.Context) error
Resume(ctx context.Context) error
```

The engine may pause and resume a source (e.g., via the OAM gRPC API). Update your internal state accordingly. If pausing is not meaningful for your source, just track the state.

### GetLag

```go
GetLag() LagInfo
```

Return health and lag information. `LagInfo` has two fields:

- `LagBytes int64` -- byte-level lag if available (e.g., WAL lag for PostgreSQL).
- `LagTime *time.Duration` -- time-based lag if available.

Return an empty `LagInfo{}` if lag metrics are not available.

### OrderingGuarantee

```go
OrderingGuarantee() OrderingGuarantee
```

Tell the engine what ordering your source provides:

| Value | Meaning |
|---|---|
| `TotalOrder` | All changes in commit order (like PostgreSQL WAL). |
| `PerTableOrder` | Ordered within a table, no cross-table guarantee. |
| `PerPartitionOrder` | Ordered within a partition or shard (like Kinesis). |
| `BestEffort` | No strong ordering. |

The engine uses this to decide ACK strategies and buffer management.

### State

```go
State() SourceState
```

Return the current connection state. Manage this internally as you transition through the lifecycle:

| State | When |
|---|---|
| `SourceConnecting` | During `Init`, establishing a connection. |
| `SourceConnected` | Connected, before or after baseline. |
| `SourceStreaming` | Actively streaming changes. |
| `SourceReconnecting` | Attempting to reconnect after a failure. |
| `SourcePaused` | Paused by the engine. |
| `SourceError` | Unrecoverable error. |
| `SourceClosed` | Shut down. |

### Close

```go
Close(ctx context.Context) error
```

Clean up connections and resources. Called once during engine shutdown.

## Position type requirements

The `Position` type is `any`, but the engine relies on three things:

1. **Comparability** -- `ComparePositions` must produce a total order over your positions.
2. **Serializability** -- `PositionToString` and `PositionFromString` must round-trip without loss.
3. **Monotonicity** -- positions returned from `Baseline` and from `ChangeEvent.Position` must be monotonically increasing.

Common choices:

- `uint64` for a simple sequence counter (used by `testsource`).
- A custom `LSN` type for PostgreSQL WAL positions.
- A string like `"shardId-000:12345"` for Kinesis.

## Stateful vs. ephemeral sources

A **stateful** source persists its ACKed position externally (e.g., in the database, in a replication slot, in a checkpoint file). On restart, it returns the saved position from `LastAckedPosition` and the engine skips the baseline.

An **ephemeral** source does not persist position. `SupportsResume()` returns `false`, `LastAckedPosition` returns `nil`, and the engine runs a full baseline on every startup.

You can support both modes in a single implementation (like the PostgreSQL source does with `SlotEphemeral` and `SlotStateful`).

## Example: minimal CSV file source

This example implements a source that reads rows from CSV files (one per table) for the baseline and watches for new lines appended to a changes file for streaming.

```go
package csvsource

import (
    "bufio"
    "context"
    "encoding/csv"
    "fmt"
    "os"
    "strconv"
    "sync"
    "time"

    "github.com/zourzouvillys/laredo"
)

// Source reads baseline rows from CSV files and streams appended lines.
type Source struct {
    mu       sync.Mutex
    dir      string       // directory containing table CSV files
    state    laredo.SourceState
    seq      uint64
    lastAck  *uint64
    schemas  map[laredo.TableIdentifier][]laredo.ColumnDefinition
}

func New(dir string) *Source {
    return &Source{
        dir:     dir,
        state:   laredo.SourceClosed,
        schemas: make(map[laredo.TableIdentifier][]laredo.ColumnDefinition),
    }
}

// Compile-time interface check.
var _ laredo.SyncSource = (*Source)(nil)

func (s *Source) Init(_ context.Context, config laredo.SourceConfig) (map[laredo.TableIdentifier][]laredo.ColumnDefinition, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    s.state = laredo.SourceConnecting

    result := make(map[laredo.TableIdentifier][]laredo.ColumnDefinition)
    for _, table := range config.Tables {
        // Read the header row of each CSV to discover columns.
        path := fmt.Sprintf("%s/%s.%s.csv", s.dir, table.Schema, table.Table)
        f, err := os.Open(path)
        if err != nil {
            s.state = laredo.SourceError
            return nil, fmt.Errorf("open %s: %w", path, err)
        }
        reader := csv.NewReader(f)
        header, err := reader.Read()
        f.Close()
        if err != nil {
            s.state = laredo.SourceError
            return nil, fmt.Errorf("read header from %s: %w", path, err)
        }

        cols := make([]laredo.ColumnDefinition, len(header))
        for i, name := range header {
            cols[i] = laredo.ColumnDefinition{
                Name:            name,
                Type:            "text",
                OrdinalPosition: i + 1,
                PrimaryKey:      i == 0, // first column is PK
                PrimaryKeyOrdinal: func() int {
                    if i == 0 { return 1 }
                    return 0
                }(),
            }
        }
        result[table] = cols
        s.schemas[table] = cols
    }

    s.state = laredo.SourceConnected
    return result, nil
}

func (s *Source) ValidateTables(_ context.Context, tables []laredo.TableIdentifier) []laredo.ValidationError {
    var errs []laredo.ValidationError
    for _, table := range tables {
        path := fmt.Sprintf("%s/%s.%s.csv", s.dir, table.Schema, table.Table)
        if _, err := os.Stat(path); err != nil {
            t := table
            errs = append(errs, laredo.ValidationError{
                Table:   &t,
                Code:    "FILE_NOT_FOUND",
                Message: fmt.Sprintf("CSV file not found: %s", path),
            })
        }
    }
    return errs
}

func (s *Source) Baseline(_ context.Context, tables []laredo.TableIdentifier, rowCallback func(laredo.TableIdentifier, laredo.Row)) (laredo.Position, error) {
    for _, table := range tables {
        path := fmt.Sprintf("%s/%s.%s.csv", s.dir, table.Schema, table.Table)
        f, err := os.Open(path)
        if err != nil {
            return nil, fmt.Errorf("open %s: %w", path, err)
        }

        reader := csv.NewReader(f)
        header, _ := reader.Read() // skip header

        for {
            record, err := reader.Read()
            if err != nil {
                break
            }
            row := make(laredo.Row, len(header))
            for i, name := range header {
                row[name] = record[i]
            }
            rowCallback(table, row)
        }
        f.Close()
    }

    s.mu.Lock()
    s.seq++
    pos := s.seq
    s.mu.Unlock()
    return pos, nil
}

func (s *Source) Stream(ctx context.Context, _ laredo.Position, handler laredo.ChangeHandler) error {
    s.mu.Lock()
    s.state = laredo.SourceStreaming
    s.mu.Unlock()

    // Watch a changes file for new lines. Each line is: action,table,col1=val1,col2=val2,...
    changesPath := fmt.Sprintf("%s/changes.log", s.dir)
    f, err := os.Open(changesPath)
    if err != nil {
        // No changes file -- just block until context is cancelled.
        <-ctx.Done()
        return ctx.Err()
    }
    defer f.Close()

    // Seek to end and tail.
    _, _ = f.Seek(0, 2)
    scanner := bufio.NewScanner(f)

    for {
        select {
        case <-ctx.Done():
            s.mu.Lock()
            s.state = laredo.SourceClosed
            s.mu.Unlock()
            return ctx.Err()
        case <-time.After(100 * time.Millisecond):
            for scanner.Scan() {
                s.mu.Lock()
                s.seq++
                pos := s.seq
                s.mu.Unlock()

                event := laredo.ChangeEvent{
                    Action:    laredo.ActionInsert,
                    Position:  pos,
                    Timestamp: time.Now(),
                    // Parse the line into NewValues here...
                }
                if err := handler.OnChange(event); err != nil {
                    return fmt.Errorf("handler error: %w", err)
                }
            }
        }
    }
}

func (s *Source) Ack(_ context.Context, position laredo.Position) error {
    pos, ok := position.(uint64)
    if !ok {
        return fmt.Errorf("expected uint64 position, got %T", position)
    }
    s.mu.Lock()
    s.lastAck = &pos
    s.mu.Unlock()
    return nil
}

func (s *Source) SupportsResume() bool    { return false }

func (s *Source) LastAckedPosition(_ context.Context) (laredo.Position, error) {
    return nil, nil
}

func (s *Source) ComparePositions(a, b laredo.Position) int {
    posA, _ := a.(uint64)
    posB, _ := b.(uint64)
    switch {
    case posA < posB:
        return -1
    case posA > posB:
        return 1
    default:
        return 0
    }
}

func (s *Source) PositionToString(p laredo.Position) string {
    if pos, ok := p.(uint64); ok {
        return strconv.FormatUint(pos, 10)
    }
    return ""
}

func (s *Source) PositionFromString(str string) (laredo.Position, error) {
    pos, err := strconv.ParseUint(str, 10, 64)
    if err != nil {
        return nil, fmt.Errorf("invalid position %q: %w", str, err)
    }
    return pos, nil
}

func (s *Source) Pause(_ context.Context) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.state = laredo.SourcePaused
    return nil
}

func (s *Source) Resume(_ context.Context) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.state = laredo.SourceStreaming
    return nil
}

func (s *Source) GetLag() laredo.LagInfo            { return laredo.LagInfo{} }
func (s *Source) OrderingGuarantee() laredo.OrderingGuarantee { return laredo.TotalOrder }

func (s *Source) State() laredo.SourceState {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.state
}

func (s *Source) Close(_ context.Context) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.state = laredo.SourceClosed
    return nil
}
```

## Wiring into the engine

Once you have your source, register it with the engine and bind pipelines to it:

```go
src := csvsource.New("/data/csv")
target := memory.NewIndexedTarget()

eng, err := laredo.NewEngine(
    laredo.WithSource("csv", src),
    laredo.WithPipeline("csv", laredo.Table("public", "users"), target),
)
if err != nil {
    log.Fatal(err)
}

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

if err := eng.Start(ctx); err != nil {
    log.Fatal(err)
}

eng.AwaitReady(30 * time.Second)
```

## Optional: Resettable sources

If your source supports a destructive reset operation (e.g., dropping and recreating a replication slot), implement the `Resettable` interface:

```go
type Resettable interface {
    ResetSource(ctx context.Context, dropPublication bool) error
}
```

This enables the `laredo reset-source` CLI command and the `ResetSource` OAM gRPC RPC. The PostgreSQL source uses this to drop and recreate its replication slot.

## Testing your source

Use the engine directly in tests -- no mocks needed. Create your source, wire it to an in-memory target, start the engine, and assert on the target contents:

```go
func TestCustomSource(t *testing.T) {
    src := csvsource.New("testdata")
    target := memory.NewIndexedTarget()

    eng, err := laredo.NewEngine(
        laredo.WithSource("test", src),
        laredo.WithPipeline("test", laredo.Table("public", "users"), target),
    )
    if err != nil {
        t.Fatal(err)
    }

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    if err := eng.Start(ctx); err != nil {
        t.Fatal(err)
    }
    if !eng.AwaitReady(5 * time.Second) {
        t.Fatal("engine did not become ready")
    }

    // Assert baseline was loaded.
    if target.Count() == 0 {
        t.Error("expected rows after baseline")
    }
}
```

For a working reference, see the [`source/testsource`](/concepts/sources) package -- it implements the full interface with in-memory data and programmable change injection.

## Checklist

Before using your custom source in production:

- [ ] All 16 interface methods are implemented.
- [ ] `Init` returns correct column definitions including primary key metadata (`PrimaryKey`, `PrimaryKeyOrdinal`).
- [ ] `Baseline` delivers a consistent snapshot and returns a valid position.
- [ ] `Stream` blocks until context cancellation and returns `ctx.Err()` on clean shutdown.
- [ ] `Stream` calls `handler.OnChange` with monotonically increasing positions.
- [ ] `ComparePositions`, `PositionToString`, and `PositionFromString` are consistent with each other.
- [ ] `State()` reflects the current lifecycle phase.
- [ ] `Close` releases all resources.
- [ ] Thread safety: concurrent calls to `State()`, `GetLag()`, `Ack()`, `Pause()`, and `Resume()` are safe.
