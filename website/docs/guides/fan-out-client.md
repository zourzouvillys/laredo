---
sidebar_position: 5
title: Fan-Out Client Library
---

# Fan-Out Client Library

The `client/fanout` package is a Go client that connects to a laredo fan-out replication service and maintains a local in-memory replica of a table. It handles the full sync protocol -- initial snapshot, delta catch-up, and live streaming -- so your application gets a queryable, always-up-to-date copy of the data without managing replication directly.

## When to use it

Use the fan-out client when:

- Your service needs a read-only copy of a table from another laredo instance.
- You want to avoid giving every service instance its own PostgreSQL replication slot.
- You need sub-second propagation of changes to downstream services.
- You want a simple Go API for querying replicated data without running an embedded engine.

The fan-out client pairs with a [fan-out target](/guides/fan-out) running on the server side. The server holds the replication slot and multiplexes changes to all connected clients over gRPC.

## Install

```bash
go get github.com/zourzouvillys/laredo
```

The client lives in the `client/fanout` subpackage:

```go
import "github.com/zourzouvillys/laredo/client/fanout"
```

## Quick start

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "time"

    "github.com/zourzouvillys/laredo/client/fanout"
)

func main() {
    client := fanout.New(
        fanout.ServerAddress("laredo-server:4002"),
        fanout.Table("public", "config_document"),
        fanout.ClientID("myapp-instance-1"),
    )

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
    defer stop()

    if err := client.Start(ctx); err != nil {
        log.Fatalf("failed to start: %v", err)
    }
    defer client.Stop()

    if !client.AwaitReady(30 * time.Second) {
        log.Fatal("timed out waiting for initial sync")
    }

    log.Printf("replica ready: %d rows", client.Count())

    row, ok := client.Get("42")
    if ok {
        log.Printf("row 42: %v", row)
    }

    // Block until interrupted.
    <-ctx.Done()
}
```

## Configuration options

All configuration is passed as functional options to `fanout.New`.

### ServerAddress

Sets the address of the fan-out gRPC server. This is the `host:port` where the fan-out target is listening (configured via `grpc.port` on the server side).

```go
fanout.ServerAddress("laredo-server:4002")
```

### Table

Specifies the schema and table name to replicate. Must match a table being served by the fan-out target on the server.

```go
fanout.Table("public", "config_document")
```

### ClientID

An identifier for this client instance, used for server-side monitoring and logging. Set this to something that uniquely identifies the running instance (e.g., hostname, pod name, or service instance ID).

```go
fanout.ClientID("myapp-pod-abc123")
```

## Lifecycle

### Start

`Start` connects to the server and begins receiving data in a background goroutine. It is non-blocking and returns immediately.

```go
if err := client.Start(ctx); err != nil {
    log.Fatalf("start failed: %v", err)
}
```

The provided context controls the lifetime of the client. Cancelling the context triggers a graceful shutdown equivalent to calling `Stop`.

### AwaitReady

`AwaitReady` blocks until the client has received its initial state (either a full snapshot or a delta catch-up) or the timeout expires. Returns `true` if the client is ready, `false` if it timed out.

```go
if !client.AwaitReady(30 * time.Second) {
    log.Fatal("initial sync timed out")
}
```

After `AwaitReady` returns `true`, all query methods return consistent data. Before that point, results may be incomplete.

### Stop

`Stop` disconnects from the server and waits for the background goroutine to exit. It blocks until shutdown is complete.

```go
client.Stop()
```

Always call `Stop` to clean up resources. A common pattern is `defer client.Stop()` right after `Start`.

## Querying

All query methods are safe to call concurrently from multiple goroutines.

### Get

Returns a single row by its `id` field value. Returns `false` if the row does not exist.

```go
row, ok := client.Get("42")
if ok {
    log.Printf("name: %v", row["name"])
}
```

Rows are keyed by their `id` field. If the row does not have an `id` field, the key is derived from all field values.

### Lookup

Scans all rows to find one matching a field value. This is an O(n) operation. Returns the first match, or `false` if none found.

```go
row, ok := client.Lookup("email", "alice@example.com")
if ok {
    log.Printf("found user: %v", row["id"])
}
```

For frequent lookups on non-primary-key fields, consider using the embedded engine with an [indexed in-memory target](/guides/in-memory-targets) instead.

### All

Returns a copy of all rows in the local replica as a slice.

```go
rows := client.All()
for _, row := range rows {
    log.Printf("id=%v name=%v", row["id"], row["name"])
}
```

### Count

Returns the number of rows in the local replica.

```go
log.Printf("replica has %d rows", client.Count())
```

## Listening for changes

Register a listener to react to changes as they arrive. The listener receives the old and new row values:

- **Insert**: `old` is `nil`, `new` is the inserted row.
- **Update**: `old` is the previous row, `new` is the updated row.
- **Delete**: `old` is the deleted row, `new` is `nil`.
- **Truncate**: both `old` and `new` are `nil`.

```go
unsub := client.Listen(func(old, new laredo.Row) {
    switch {
    case old == nil && new != nil:
        log.Printf("inserted: %v", new["id"])
    case old != nil && new != nil:
        log.Printf("updated: %v", new["id"])
    case old != nil && new == nil:
        log.Printf("deleted: %v", old["id"])
    default:
        log.Printf("table truncated")
    }
})

// Later, when you no longer need notifications:
unsub()
```

`Listen` returns an unsubscribe function. Call it to remove the listener. Only one listener can be active at a time -- calling `Listen` again replaces the previous listener.

The listener is called synchronously while the client holds its internal lock. Keep listener functions fast and non-blocking. If you need to do expensive work, dispatch it to a separate goroutine.

## Reconnect behavior

The client automatically reconnects if the connection drops. Reconnection uses exponential backoff:

- Initial delay: **1 second**
- Maximum delay: **30 seconds**
- Backoff doubles on each consecutive failure

On reconnect, the client sends its `LastSequence` to the server. The server decides whether to send a delta (journal entries since that sequence) or a full snapshot (if the client is too far behind). This means:

- **Short disconnections** result in a fast delta catch-up with no data loss.
- **Long disconnections** (beyond journal retention) trigger a full re-snapshot automatically.
- **Local state is preserved** across reconnections. Rows from the previous session remain available while the client reconnects.

No application code is needed to handle reconnections. The client manages it transparently.

## Monitoring with LastSequence

`LastSequence` returns the last journal sequence number received from the server. Use it to monitor replication lag or to check that the client is keeping up.

```go
seq := client.LastSequence()
log.Printf("last sequence: %d", seq)
```

You can poll this value to build a health check or expose it as a metric:

```go
// Example: expose as a Prometheus gauge
go func() {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        replicaSequence.Set(float64(client.LastSequence()))
    }
}()
```

A sequence of `0` means no data has been received yet.

## Full example

```go
package main

import (
    "context"
    "log"
    "net/http"
    "os"
    "os/signal"
    "time"

    "github.com/zourzouvillys/laredo"
    "github.com/zourzouvillys/laredo/client/fanout"
)

func main() {
    client := fanout.New(
        fanout.ServerAddress("laredo-server:4002"),
        fanout.Table("public", "users"),
        fanout.ClientID("api-server-1"),
    )

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
    defer stop()

    if err := client.Start(ctx); err != nil {
        log.Fatalf("start: %v", err)
    }
    defer client.Stop()

    // Wait for initial data.
    if !client.AwaitReady(30 * time.Second) {
        log.Fatal("initial sync timed out")
    }

    // React to changes.
    unsub := client.Listen(func(old, new laredo.Row) {
        if new != nil {
            log.Printf("user changed: %v", new["id"])
        }
    })
    defer unsub()

    // Serve HTTP using the replica.
    http.HandleFunc("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        row, ok := client.Get(id)
        if !ok {
            http.NotFound(w, r)
            return
        }
        fmt.Fprintf(w, "user %s: %v\n", id, row["name"])
    })

    http.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
        rows := client.All()
        fmt.Fprintf(w, "%d users\n", len(rows))
    })

    log.Printf("serving on :8080 with %d users", client.Count())
    srv := &http.Server{Addr: ":8080", ReadHeaderTimeout: 10 * time.Second}
    go func() { _ = srv.ListenAndServe() }()

    <-ctx.Done()
    _ = srv.Shutdown(context.Background())
}
```

## Next steps

- [Replication Fan-Out](/guides/fan-out) for server-side configuration
- [In-Memory Targets](/guides/in-memory-targets) if you need indexed lookups or compiled domain objects
- [Monitoring](/guides/monitoring) to add Prometheus or OpenTelemetry metrics
