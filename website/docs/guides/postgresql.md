---
sidebar_position: 1
title: PostgreSQL Source
---

# PostgreSQL Source

The PostgreSQL source uses logical replication with the built-in `pgoutput` plugin. No extensions needed.

## Ephemeral mode

On every startup: create a temporary replication slot, full baseline, stream changes. If the process crashes, the slot is gone and the next startup repeats the full cycle.

```hocon
sources {
  pg_main {
    type = postgresql
    connection = "postgresql://user:pass@host:5432/dbname"
    slot_mode = ephemeral
  }
}
```

Best for: development, testing, or small datasets where full reload on restart is acceptable.

## Stateful mode

Uses a persistent named replication slot that survives restarts. Resumes from the last ACKed LSN.

```hocon
sources {
  pg_main {
    type = postgresql
    connection = "postgresql://user:pass@host:5432/dbname"
    slot_mode = stateful
    slot_name = laredo_warden_01

    publication {
      name = laredo_warden_01_pub
      create = true
      publish = "insert, update, delete, truncate"
    }

    reconnect {
      max_attempts = 10
      initial_backoff = 1s
      max_backoff = 60s
      backoff_multiplier = 2.0
    }
  }
}
```

### First startup

1. Create a named persistent replication slot, exporting its snapshot
2. Full baseline that imports the slot's exported snapshot (`SET TRANSACTION SNAPSHOT`), so the COPY reads at exactly the slot's consistent point
3. Begin streaming changes from that same consistent point — no gap, no duplicates
4. ACK only after targets confirm durability

This is the same consistent-handoff mechanism as native PostgreSQL logical replication (`CREATE SUBSCRIPTION … WITH (copy_data = true)`): every row that existed before the slot is copied, and every change after it is streamed, with no overlap or loss.

### Subsequent startups

1. Connect to the existing slot
2. Resume streaming from the last ACKed LSN
3. No baseline needed

:::warning Resume only reconstructs a durable target
Resuming from the last ACKed LSN assumes the target's state **survives the restart** — for example an external database written via the HTTP sync target. An **in-memory target** (indexed/compiled memory, fan-out) is rebuilt empty on every process start, so resuming would leave it empty and only apply changes written *after* the restart. For those targets, either use **ephemeral** mode (full baseline every start), configure **[snapshots](../concepts/snapshots.md)** to persist and restore in-memory state, or enable [`always_baseline`](#always-baseline-non-durable-targets) below.
:::

## Always baseline (non-durable targets)

`always_baseline` forces a full COPY on **every** startup while still using a persistent slot. Use it when the target is not durable across restarts (e.g. loading config or rules into memory) but you still want a persistent slot so WAL is retained while the process runs.

```hocon
sources {
  pg_main {
    type = postgresql
    connection = "postgresql://user:pass@host:5432/dbname"
    slot_mode = stateful
    slot_name = laredo_config_01
    always_baseline = true
  }
}
```

On each startup the engine re-COPIES the current table contents into the (empty) target, then streams subsequent changes. Because streaming resumes from the slot's confirmed position and the COPY reflects "now", any changes in the overlap window are re-delivered rather than lost — safe for idempotent, primary-key-keyed targets.

Has no effect in ephemeral mode, which already baselines every startup.

## Publication management

When `create = true`, Laredo auto-creates and manages a publication:

- Creates the publication on first startup
- Adds/removes tables when the config changes
- Supports row filters and column lists (PostgreSQL 15+)

```hocon
publication {
  create = true
  tables {
    "public.config_document" {
      where = "status = 'active'"           # row filter (PG 15+)
    }
    "public.rules" {
      columns = [instance_id, key, data]    # column list (PG 15+)
    }
  }
}
```

## Reconnection

On connection loss during streaming, the source attempts to reconnect automatically with exponential backoff. In stateful mode, the slot retains WAL and streaming resumes from the last ACKed LSN. After `max_attempts` failures, the source transitions to ERROR.

## Library usage

```go
source := pg.New(
    pg.Connection("postgresql://user:pass@host:5432/dbname"),
    pg.SlotModeOpt(pg.SlotStateful),
    pg.SlotName("laredo_warden_01"),
    pg.Publication(pg.PublicationConfig{
        Name:   "laredo_warden_01_pub",
        Create: true,
    }),
    pg.Reconnect(pg.ReconnectConfig{
        MaxAttempts:    10,
        InitialBackoff: 1 * time.Second,
        MaxBackoff:     60 * time.Second,
        Multiplier:     2.0,
    }),
    // For non-durable (in-memory) targets, force a full COPY on every startup:
    pg.AlwaysBaseline(true),
)
```
