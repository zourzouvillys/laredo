---
sidebar_position: 7
title: Resource Sizing
---

# Resource Requirements and Sizing

This page provides guidance on resource requirements for laredo-server based on workload characteristics.

## Baseline performance

Measured with PostgreSQL 16 and Go 1.26 (Apple M5 Pro, Docker Desktop):

| Operation | Throughput | Notes |
|---|---|---|
| Baseline loading | ~148K rows/sec | 10K rows, indexed-memory target |
| Streaming changes | ~5.9K changes/sec | INSERT via logical replication |
| Fan-out insert | ~2.5M ops/sec | In-memory journal append |
| Fan-out journal read | ~120K reads/sec | 100 entries per read |
| Fan-out snapshot | ~165K snapshots/sec | 1K-row snapshots |

These numbers are from single-source, single-pipeline configurations. Multi-pipeline setups share the source connection but parallelize target delivery.

## Memory

Memory usage is dominated by the in-memory targets:

| Component | Memory per row |
|---|---|
| IndexedTarget | ~200-500 bytes (depends on row width) |
| CompiledTarget | depends on compiled object size |
| Fan-out target | ~200-500 bytes + journal entries |
| Fan-out journal entry | ~200-400 bytes |

**Rule of thumb**: plan for 1 KB per row in memory targets, plus 500 bytes per journal entry for fan-out.

### Example sizing

| Rows | Columns | Estimated memory |
|---|---|---|
| 10K | 5 | ~5 MB |
| 100K | 5 | ~50 MB |
| 1M | 5 | ~500 MB |
| 1M | 20 | ~1-2 GB |

## CPU

CPU usage comes from:

1. **Baseline loading**: CPU-bound during SELECT parsing and target insertion. Brief spike on startup.
2. **Streaming**: Low CPU. Dominated by WAL decoding and JSON/struct conversion.
3. **gRPC serving**: Low CPU per request. Scales with number of connected clients.

For most workloads, 1-2 CPU cores is sufficient. Fan-out with many connected clients may benefit from more cores.

## Disk

laredo-server itself uses minimal disk:

- **Snapshots**: proportional to table size (JSONL format, ~1 KB per row)
- **Local snapshot cache** (fan-out client): same as snapshot size
- **No WAL or durability files**: laredo relies on the source (PostgreSQL) for durability

## PostgreSQL considerations

- **Replication slots** hold back WAL. Monitor `pg_stat_replication` for lag.
- Set `max_slot_wal_keep_size` to prevent unbounded WAL growth (e.g., `1GB`).
- Each laredo source uses 1 replication slot + 1 WAL sender connection.
- Baseline runs a `SELECT *` in a `REPEATABLE READ` transaction — plan for snapshot isolation overhead on large tables.

## Network

- Baseline: bulk data transfer from PG (proportional to table size)
- Streaming: low bandwidth, proportional to change rate
- Fan-out: per-client bandwidth proportional to change rate + initial snapshot transfer

## Docker resource limits

Recommended starting point for a deployment with 100K rows:

```yaml
resources:
  requests:
    memory: 256Mi
    cpu: 250m
  limits:
    memory: 512Mi
    cpu: "1"
```

Scale memory linearly with row count. Scale CPU with change rate and number of fan-out clients.
