---
sidebar_position: 1
title: Quick Start
---

# Quick Start

Get Laredo running with a PostgreSQL source and an in-memory target in under 5 minutes.

## Prerequisites

- Go 1.23+
- PostgreSQL 14+ with `wal_level = logical`

## Install

```bash
go install github.com/zourzouvillys/laredo/cmd/laredo-server@latest
go install github.com/zourzouvillys/laredo/cmd/laredo@latest
```

## Configure PostgreSQL

Ensure logical replication is enabled:

```sql
ALTER SYSTEM SET wal_level = logical;
-- Restart PostgreSQL after this change
```

Create a test table:

```sql
CREATE TABLE public.config_document (
  id BIGSERIAL PRIMARY KEY,
  instance_id TEXT NOT NULL,
  key TEXT NOT NULL,
  data JSONB,
  created_at TIMESTAMPTZ DEFAULT now(),
  updated_at TIMESTAMPTZ
);

INSERT INTO public.config_document (instance_id, key, data)
VALUES
  ('inst_abc', 'settings/default', '{"theme": "dark"}'),
  ('inst_abc', 'settings/notifications', '{"email": true}'),
  ('inst_xyz', 'settings/default', '{"theme": "light"}');
```

## Write a minimal config

Create `laredo.conf`:

```hocon
sources {
  pg_main {
    type = postgresql
    connection = "postgresql://user:pass@localhost:5432/mydb"
    slot_mode = ephemeral
  }
}

tables = [
  {
    source = pg_main
    schema = public
    table = config_document
    targets = [
      {
        type = indexed-memory
        lookup_fields = [instance_id, key]
      }
    ]
  }
]
```

## Start the server

```bash
laredo-server --config laredo.conf
```

## Query the data

In another terminal:

```bash
# Check status
laredo status

# Look up a row
laredo query public.config_document inst_abc settings/default

# Watch for changes
laredo watch public.config_document
```

Now insert or update a row in PostgreSQL — you'll see it appear in the watch stream immediately.

## Next steps

- [Library Usage](/getting-started/library-usage) to embed Laredo in your Go application
- [Docker](/getting-started/docker) to run with Docker Compose
- [PostgreSQL Source](/guides/postgresql) for stateful mode with persistent replication slots
