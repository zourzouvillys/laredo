---
sidebar_position: 2
title: In-Memory Targets
---

# In-Memory Targets

Laredo provides two in-memory target types for different use cases.

## Indexed In-Memory

A schema-agnostic in-memory table replica with configurable secondary indexes. Stores raw `Row` maps.

### Configuration

```hocon
targets = [{
  type = indexed-memory
  lookup_fields = [instance_id, key]
  additional_indexes = [
    { name = by_instance, fields = [instance_id], unique = false }
    { name = by_key_version, fields = [key, version], unique = true }
  ]
}]
```

### Library usage

```go
target := memory.NewIndexedTarget(
    memory.LookupFields("instance_id", "key"),
    memory.AddIndex(laredo.IndexDefinition{Name: "by_instance", Fields: []string{"instance_id"}}),
)
```

### Query API

```go
// Lookup by primary index (unique)
row, ok := target.Lookup("inst_abc", "settings/default")

// Lookup by named index (non-unique)
rows := target.LookupAll("by_instance", "inst_abc")

// Get by primary key
row, ok := target.Get(42)

// All rows (returns iter.Seq2[string, Row])
for key, row := range target.All() {
    fmt.Printf("pk=%s: %v\n", key, row)
}

// Row count
count := target.Count()

// Subscribe to changes
unsubscribe := target.Listen(func(old, new laredo.Row) {
    // old is nil on insert, new is nil on delete
})
```

## Compiled In-Memory

Deserializes each row into a strongly-typed domain object via a pluggable compiler function.

### Configuration

```hocon
targets = [{
  type = compiled-memory
  compiler = ruleset
  key_fields = [instance_id, key]
  filter {
    field = key
    prefix = "rulesets/"
  }
}]
```

### Library usage

```go
target := memory.NewCompiledTarget(
    memory.Compiler(func(row laredo.Row) (any, error) {
        return parseRuleset(row)
    }),
    memory.KeyFields("instance_id", "key"),
)
```

### Query API

```go
// Lookup by key fields
obj, ok := target.Get("inst_abc", "rulesets/default")

// All compiled objects (returns iter.Seq2[string, any])
for key, obj := range target.All() {
    fmt.Printf("pk=%s: %v\n", key, obj)
}

// Row count
count := target.Count()

// Subscribe to changes (old/new are compiled domain objects)
unsubscribe := target.Listen(func(old, new any) {
    // old is nil on insert, new is nil on delete
})
```

## Schema changes

Both in-memory targets default to `RE_BASELINE` on schema changes — the safest option. If your application can handle schema evolution, implement a custom target.

## Thread safety

Both targets are safe for concurrent reads during streaming. The engine writes from a single goroutine; reads are protected by `sync.RWMutex`.
