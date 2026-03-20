---
sidebar_position: 5
title: Filters & Transforms
---

# Filters & Transforms

Filters and transforms sit between the source and target in each pipeline. The engine applies the chain before dispatching to the target.

## Filters

A filter returns `true` to include a row, `false` to skip it. Applied to both baseline rows and change events.

### Built-in filters

| Type | Config name | Description |
|---|---|---|
| `FieldEquals` | `field-equals` | Match rows where a field equals a value |
| `FieldPrefix` | `field-prefix` | Match rows where a string field starts with a prefix |
| `FieldRegex` | `field-regex` | Match rows where a field matches a regex |

### Configuration

```hocon
filters = [
  { type = field-equals, field = status, value = active }
  { type = field-prefix, field = key, prefix = "rulesets/" }
]
```

### Custom filters (library)

```go
laredo.PipelineFilterOpt(laredo.PipelineFilterFunc(
    func(table laredo.TableIdentifier, row laredo.Row) bool {
        return row.GetString("status") == "active"
    },
))
```

## Transforms

A transform modifies a row before it reaches the target. Return `nil` to drop the row entirely.

### Built-in transforms

| Type | Config name | Description |
|---|---|---|
| `DropFields` | `drop-fields` | Remove specified fields from the row |
| `RenameFields` | `rename-fields` | Rename fields |
| `AddTimestamp` | `add-timestamp` | Add a field with the current timestamp |

### Configuration

```hocon
transforms = [
  { type = drop-fields, fields = [ssn, internal_notes] }
  { type = rename-fields, mapping = { old_name = new_name } }
  { type = add-timestamp, field = synced_at }
]
```

### Custom transforms (library)

```go
laredo.PipelineTransformOpt(laredo.PipelineTransformFunc(
    func(table laredo.TableIdentifier, row laredo.Row) laredo.Row {
        return row.Without("ssn", "internal_notes")
    },
))
```

## Chain ordering

Filters and transforms are applied in order. Filters run first (skip rows early), then transforms modify the surviving rows.

## Publication-level vs. pipeline-level

PostgreSQL 15+ supports row filters and column lists at the publication level, which reduces WAL volume at the source. Pipeline filters are independent and handle any additional per-target filtering. Use both when appropriate.
