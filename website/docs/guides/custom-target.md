---
sidebar_position: 11
title: Custom Target Implementation
---

# Custom Target Implementation

Laredo targets are pluggable. If the built-in targets (indexed memory, compiled memory, HTTP sync, fan-out) do not fit your use case, you can implement the `SyncTarget` interface and wire it into the engine.

This guide walks through the interface, the lifecycle the engine drives, snapshot support, schema change handling, and a complete minimal example.

## The SyncTarget interface

```go
type SyncTarget interface {
    OnInit(ctx context.Context, table TableIdentifier, columns []ColumnDefinition) error
    OnBaselineRow(ctx context.Context, table TableIdentifier, row Row) error
    OnBaselineComplete(ctx context.Context, table TableIdentifier) error
    OnInsert(ctx context.Context, table TableIdentifier, columns Row) error
    OnUpdate(ctx context.Context, table TableIdentifier, columns Row, identity Row) error
    OnDelete(ctx context.Context, table TableIdentifier, identity Row) error
    OnTruncate(ctx context.Context, table TableIdentifier) error
    IsDurable() bool
    OnSchemaChange(ctx context.Context, table TableIdentifier, oldColumns, newColumns []ColumnDefinition) SchemaChangeResponse
    ExportSnapshot(ctx context.Context) ([]SnapshotEntry, error)
    RestoreSnapshot(ctx context.Context, metadata TableSnapshotInfo, entries []SnapshotEntry) error
    SupportsConsistentSnapshot() bool
    OnClose(ctx context.Context, table TableIdentifier) error
}
```

Each method is described below in the order the engine calls them.

## Lifecycle

The engine drives each target through a fixed sequence:

```
OnInit -> OnBaselineRow (repeated) -> OnBaselineComplete
                                           |
                                     Live streaming:
                                       OnInsert
                                       OnUpdate
                                       OnDelete
                                       OnTruncate
                                           |
                                     (on schema change)
                                       OnSchemaChange
                                           |
                                     (on shutdown)
                                       OnClose
```

A target is bound to exactly one table in one pipeline. The engine creates one target instance per pipeline.

### OnInit

```go
OnInit(ctx context.Context, table TableIdentifier, columns []ColumnDefinition) error
```

Called once before any rows are delivered. Receives the table identifier and the full column schema discovered by the source. Use this to:

- Store the schema for later use.
- Validate that required columns exist (e.g., primary key fields, lookup fields).
- Allocate data structures (maps, indexes, buffers).
- Open connections to external systems.

The `ColumnDefinition` struct includes:

| Field | Type | Description |
|---|---|---|
| `Name` | `string` | Column name. |
| `Type` | `string` | Source type name (e.g., `"text"`, `"integer"`). |
| `Nullable` | `bool` | Whether the column allows NULL. |
| `PrimaryKey` | `bool` | Whether this column is part of the primary key. |
| `PrimaryKeyOrdinal` | `int` | Position within a composite PK (1-based). 0 if not a PK column. |
| `OrdinalPosition` | `int` | Column position in the table (1-based). |

Return an error to abort the pipeline.

### OnBaselineRow

```go
OnBaselineRow(ctx context.Context, table TableIdentifier, row Row) error
```

Called once for each row during the baseline snapshot phase. A `Row` is `map[string]Value` where `Value` is `any`. The row contains all columns from the schema.

Store or forward the row. Return an error to signal a failure -- the engine will apply the pipeline's error policy.

### OnBaselineComplete

```go
OnBaselineComplete(ctx context.Context, table TableIdentifier) error
```

Called after all baseline rows have been delivered. Use this to:

- Finalize indexes or sort data structures.
- Flush buffered rows to an external system.
- Log baseline statistics.

Many targets (like the in-memory targets) do nothing here and return `nil`.

### OnInsert

```go
OnInsert(ctx context.Context, table TableIdentifier, columns Row) error
```

A new row was inserted from the change stream. The `columns` map contains the full new row.

### OnUpdate

```go
OnUpdate(ctx context.Context, table TableIdentifier, columns Row, identity Row) error
```

An existing row was updated. Two arguments:

- `columns` -- the full row after the update (new values).
- `identity` -- the row identity before the update. Contains at minimum the primary key fields. Use this to locate and remove the old row before inserting the new one, especially when the primary key itself may have changed.

### OnDelete

```go
OnDelete(ctx context.Context, table TableIdentifier, identity Row) error
```

A row was deleted. The `identity` map contains at minimum the primary key fields needed to locate and remove the row.

### OnTruncate

```go
OnTruncate(ctx context.Context, table TableIdentifier) error
```

The entire table was truncated. Remove all rows.

### IsDurable

```go
IsDurable() bool
```

The engine calls this after delivering changes to determine whether it is safe to advance the ACK position on the source. Return `true` if the last applied change has been durably persisted (or if your target is the final store, like an in-memory map). Return `false` if there are buffered changes that have not yet been flushed to a durable backend.

The HTTP sync target, for example, returns `false` while changes are buffered and `true` only after a successful flush.

In-memory targets always return `true` because the memory itself is the store.

### OnSchemaChange

```go
OnSchemaChange(ctx context.Context, table TableIdentifier, oldColumns, newColumns []ColumnDefinition) SchemaChangeResponse
```

Called when the source detects a column schema change (e.g., a column was added or dropped). Return one of three responses:

| Action | Meaning |
|---|---|
| `SchemaContinue` | The target adapted to the new schema. Keep streaming. |
| `SchemaReBaseline` | The target cannot adapt. The engine will re-run the full baseline. |
| `SchemaError` | The target cannot handle this change at all. The pipeline enters ERROR state. |

A common strategy: if all old columns are still present and only new columns were added, return `SchemaContinue`. If columns were removed or renamed, return `SchemaReBaseline`.

```go
func (t *MyTarget) OnSchemaChange(_ context.Context, _ laredo.TableIdentifier, oldColumns, newColumns []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
    newSet := make(map[string]struct{}, len(newColumns))
    for _, c := range newColumns {
        newSet[c.Name] = struct{}{}
    }
    for _, c := range oldColumns {
        if _, ok := newSet[c.Name]; !ok {
            // A column was removed -- need a full reload.
            return laredo.SchemaChangeResponse{Action: laredo.SchemaReBaseline}
        }
    }
    return laredo.SchemaChangeResponse{Action: laredo.SchemaContinue, Message: "new columns added"}
}
```

### OnClose

```go
OnClose(ctx context.Context, table TableIdentifier) error
```

Called once during engine shutdown. Clean up resources, close connections, flush buffers.

## Snapshot support

Targets optionally support exporting and restoring snapshots. Snapshots let the engine skip the baseline on restart by loading previously saved state.

### ExportSnapshot

```go
ExportSnapshot(ctx context.Context) ([]SnapshotEntry, error)
```

Return all current rows as a slice of `SnapshotEntry` values. Each entry wraps a `Row`:

```go
type SnapshotEntry struct {
    Row Row
}
```

If your target does not support snapshots, return `nil, nil`.

### RestoreSnapshot

```go
RestoreSnapshot(ctx context.Context, metadata TableSnapshotInfo, entries []SnapshotEntry) error
```

Restore state from a previously exported snapshot. The `metadata` provides context about the snapshot (table, row count, column definitions). Clear any existing data and reload from `entries`.

If your target does not support snapshots, return `nil` (no-op).

### SupportsConsistentSnapshot

```go
SupportsConsistentSnapshot() bool
```

Return `true` if `ExportSnapshot` can produce a consistent view without the engine pausing the change stream. In-memory targets with read locks return `true`. Targets that need the stream paused return `false`.

## Example: logging target

This example implements a target that logs every row change to an `io.Writer`. It demonstrates the full interface with no snapshotting.

```go
package logtarget

import (
    "context"
    "fmt"
    "io"
    "sync"

    "github.com/zourzouvillys/laredo"
)

// Target logs all row changes to an io.Writer.
type Target struct {
    mu      sync.Mutex
    w       io.Writer
    table   laredo.TableIdentifier
    columns []laredo.ColumnDefinition
    count   int64
}

func New(w io.Writer) *Target {
    return &Target{w: w}
}

// Compile-time interface check.
var _ laredo.SyncTarget = (*Target)(nil)

func (t *Target) OnInit(_ context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.table = table
    t.columns = columns
    fmt.Fprintf(t.w, "INIT %s (%d columns)\n", table, len(columns))
    return nil
}

func (t *Target) OnBaselineRow(_ context.Context, table laredo.TableIdentifier, row laredo.Row) error {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.count++
    fmt.Fprintf(t.w, "BASELINE %s row=%v\n", table, row)
    return nil
}

func (t *Target) OnBaselineComplete(_ context.Context, table laredo.TableIdentifier) error {
    t.mu.Lock()
    defer t.mu.Unlock()
    fmt.Fprintf(t.w, "BASELINE_COMPLETE %s (%d rows)\n", table, t.count)
    return nil
}

func (t *Target) OnInsert(_ context.Context, table laredo.TableIdentifier, columns laredo.Row) error {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.count++
    fmt.Fprintf(t.w, "INSERT %s row=%v\n", table, columns)
    return nil
}

func (t *Target) OnUpdate(_ context.Context, table laredo.TableIdentifier, columns laredo.Row, identity laredo.Row) error {
    t.mu.Lock()
    defer t.mu.Unlock()
    fmt.Fprintf(t.w, "UPDATE %s new=%v old=%v\n", table, columns, identity)
    return nil
}

func (t *Target) OnDelete(_ context.Context, table laredo.TableIdentifier, identity laredo.Row) error {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.count--
    fmt.Fprintf(t.w, "DELETE %s identity=%v\n", table, identity)
    return nil
}

func (t *Target) OnTruncate(_ context.Context, table laredo.TableIdentifier) error {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.count = 0
    fmt.Fprintf(t.w, "TRUNCATE %s\n", table)
    return nil
}

func (t *Target) IsDurable() bool { return true }

func (t *Target) OnSchemaChange(_ context.Context, _ laredo.TableIdentifier, _, _ []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
    // Log targets can handle any schema change -- just keep logging.
    return laredo.SchemaChangeResponse{Action: laredo.SchemaContinue}
}

func (t *Target) ExportSnapshot(_ context.Context) ([]laredo.SnapshotEntry, error) {
    return nil, nil // No snapshot support.
}

func (t *Target) RestoreSnapshot(_ context.Context, _ laredo.TableSnapshotInfo, _ []laredo.SnapshotEntry) error {
    return nil // No snapshot support.
}

func (t *Target) SupportsConsistentSnapshot() bool { return false }

func (t *Target) OnClose(_ context.Context, table laredo.TableIdentifier) error {
    t.mu.Lock()
    defer t.mu.Unlock()
    fmt.Fprintf(t.w, "CLOSE %s\n", table)
    return nil
}
```

## Wiring into the engine

Register the target in a pipeline:

```go
logTarget := logtarget.New(os.Stdout)

eng, err := laredo.NewEngine(
    laredo.WithSource("pg", pgSource),
    laredo.WithPipeline("pg", laredo.Table("public", "users"), logTarget),
)
```

A single source can fan out to multiple targets for the same table. Each target gets its own pipeline:

```go
eng, err := laredo.NewEngine(
    laredo.WithSource("pg", pgSource),
    laredo.WithPipeline("pg", laredo.Table("public", "users"), memTarget),
    laredo.WithPipeline("pg", laredo.Table("public", "users"), logTarget),
    laredo.WithPipeline("pg", laredo.Table("public", "users"), httpTarget),
)
```

## Pipeline options

When binding a target to a pipeline, you can configure additional behavior:

```go
laredo.WithPipeline("pg", table, target,
    laredo.PipelineFilterOpt(filter.FieldEquals("status", "active")),
    laredo.PipelineTransformOpt(transform.DropFields("internal_id")),
    laredo.BufferSize(50000),
    laredo.ErrorPolicyOpt(laredo.ErrorIsolate),
    laredo.MaxRetries(3),
)
```

Filters and transforms run before the row reaches your target, so your `OnInsert`/`OnUpdate`/`OnDelete` methods receive already-filtered, already-transformed rows.

## Testing your target

Use the `source/testsource` package to drive your target through the full lifecycle without a real data source:

```go
func TestCustomTarget(t *testing.T) {
    var buf bytes.Buffer
    target := logtarget.New(&buf)

    src := testsource.New()
    table := laredo.Table("public", "users")
    src.SetSchema(table, []laredo.ColumnDefinition{
        {Name: "id", Type: "integer", PrimaryKey: true, PrimaryKeyOrdinal: 1},
        {Name: "name", Type: "text"},
    })
    src.AddRow(table, laredo.Row{"id": 1, "name": "alice"})
    src.AddRow(table, laredo.Row{"id": 2, "name": "bob"})

    eng, err := laredo.NewEngine(
        laredo.WithSource("test", src),
        laredo.WithPipeline("test", table, target),
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

    // Verify baseline was delivered.
    output := buf.String()
    if !strings.Contains(output, "BASELINE_COMPLETE") {
        t.Error("expected baseline complete in output")
    }

    // Emit a change and verify it arrives.
    src.EmitInsert(table, laredo.Row{"id": 3, "name": "charlie"})
    time.Sleep(100 * time.Millisecond) // wait for delivery
    if !strings.Contains(buf.String(), "INSERT") {
        t.Error("expected INSERT in output")
    }
}
```

## Thread safety

The engine calls target methods from a single goroutine per pipeline. You do not need to protect `OnInit`, `OnBaselineRow`, `OnInsert`, `OnUpdate`, `OnDelete`, and `OnTruncate` from concurrent calls -- they are always sequential.

However, if your target exposes a query API (like the in-memory targets' `Get`, `Lookup`, `All` methods), you need a `sync.RWMutex` to protect reads from concurrent writes. The pattern is:

- Write methods (`OnInsert`, etc.) take the write lock.
- Read methods (`Get`, `Count`, etc.) take the read lock.

See the `target/memory` package for the canonical implementation of this pattern.

## Checklist

Before using your custom target in production:

- [ ] All 13 interface methods are implemented.
- [ ] `OnInit` validates any required fields (primary key columns, lookup fields) and returns an error if they are missing.
- [ ] `OnUpdate` correctly handles the `identity` parameter to locate the old row, even when the primary key changes.
- [ ] `OnTruncate` removes all rows.
- [ ] `IsDurable` accurately reflects whether changes have been persisted. Returning `true` prematurely can cause data loss on crash.
- [ ] `OnSchemaChange` returns the appropriate action for your target's capabilities.
- [ ] If snapshots are supported, `ExportSnapshot` and `RestoreSnapshot` round-trip correctly.
- [ ] `OnClose` releases all resources and flushes any remaining buffers.
