---
sidebar_position: 2
title: Library Usage
---

# Library Usage

Embed Laredo in your Go application for direct programmatic access to in-memory data.

## Install the module

```bash
go get github.com/zourzouvillys/laredo
```

## Minimal example

```go
package main

import (
    "log"
    "time"

    "github.com/zourzouvillys/laredo"
    "github.com/zourzouvillys/laredo/source/pg"
    "github.com/zourzouvillys/laredo/target/memory"
)

func main() {
    engine, errs := laredo.NewEngine(
        // Register a PostgreSQL source
        laredo.WithSource("pg_main", pg.New(
            pg.Connection("postgresql://user:pass@localhost:5432/mydb"),
            pg.SlotMode(pg.Ephemeral),
        )),

        // Bind a table to an indexed in-memory target
        laredo.WithPipeline("pg_main",
            laredo.Table("public", "config_document"),
            memory.NewIndexedTarget(
                memory.LookupFields("instance_id", "key"),
            ),
        ),
    )
    if len(errs) > 0 {
        log.Fatalf("config errors: %v", errs)
    }

    // Start baseline + streaming
    engine.Start(context.Background())
    defer engine.Stop(context.Background())

    // Wait until data is ready (blocking)
    engine.AwaitReady(30 * time.Second)

    // Or use a callback instead:
    // engine.OnReady(func() { log.Println("all pipelines ready") })

    // Query the in-memory replica directly
    target, ok := laredo.GetTarget[*memory.IndexedTarget](
        engine, "pg_main", laredo.Table("public", "config_document"),
    )
    if !ok {
        log.Fatal("target not found")
    }

    row, ok := target.Lookup("inst_abc", "settings/default")
    if ok {
        log.Printf("found: %v", row)
    }

    // Subscribe to live changes
    target.Listen(func(old, new laredo.Row) {
        log.Printf("change: %v -> %v", old, new)
    })

    // Block forever (or until signal)
    select {}
}
```

## Adding filters and transforms

```go
laredo.WithPipeline("pg_main",
    laredo.Table("public", "config_document"),
    memory.NewIndexedTarget(
        memory.LookupFields("instance_id", "key"),
        memory.AddIndex(laredo.IndexDefinition{Name: "by_instance", Fields: []string{"instance_id"}}),
    ),
    // Only sync active rows
    laredo.PipelineFilterOpt(laredo.PipelineFilterFunc(
        func(table laredo.TableIdentifier, row laredo.Row) bool {
            return row.GetString("status") == "active"
        },
    )),
    // Strip sensitive fields before storing
    laredo.PipelineTransformOpt(laredo.PipelineTransformFunc(
        func(table laredo.TableIdentifier, row laredo.Row) laredo.Row {
            return row.Without("ssn", "internal_notes")
        },
    )),
    laredo.BufferSize(1000),
    laredo.ErrorPolicyOpt(laredo.ErrorIsolate),
),
```

## Working with rows

`Row` is a `map[string]Value` with convenience accessors:

```go
row, ok := target.Lookup("inst_abc", "settings/default")
if !ok {
    return
}

// Get a value with presence check (distinguishes NULL from absent)
val, exists := row.Get("config_json")

// Get a string value (returns "" if missing or not a string)
name := row.GetString("name")

// Iterate columns in deterministic (sorted) order
for key := range row.Keys() {
    fmt.Printf("%s = %v\n", key, row[key])
}

// Create a copy without sensitive fields
safe := row.Without("ssn", "internal_notes")
```

## Attaching gRPC management

```go
engine.Start(ctx)

grpcServer := service.New(engine,
    service.Port(4001),
    service.EnableOAM(true),
    service.EnableQuery(true),
)
grpcServer.Start()
```

## Next steps

- [Concepts: Pipelines](/concepts/pipelines) to understand the pipeline model
- [In-Memory Targets](/guides/in-memory-targets) for indexed and compiled targets
- [Monitoring](/guides/monitoring) to add Prometheus or OpenTelemetry metrics
