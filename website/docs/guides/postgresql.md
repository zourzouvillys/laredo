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

1. Create a named persistent replication slot
2. Full baseline using the exported snapshot
3. Begin streaming changes
4. ACK only after targets confirm durability

### Subsequent startups

1. Connect to the existing slot
2. Resume streaming from the last ACKed LSN
3. No baseline needed

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
    pg.SlotMode(pg.Stateful),
    pg.SlotName("laredo_warden_01"),
    pg.Publication(
        pg.PubName("laredo_warden_01_pub"),
        pg.PubCreate(true),
    ),
    pg.Reconnect(10, 1*time.Second, 60*time.Second, 2.0),
)
```
