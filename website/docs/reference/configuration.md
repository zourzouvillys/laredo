---
sidebar_position: 1
title: Configuration
---

# Configuration Reference

Laredo uses HOCON configuration. Config is resolved in order (later overrides earlier):

1. **Built-in defaults**
2. **Config file** (`--config` flag or `LAREDO_CONFIG` env var)
3. **Config directory** (`/etc/laredo/conf.d/*.conf`, alphabetical)
4. **Environment variables** (`LAREDO_SOURCES_PG_MAIN_CONNECTION`)
5. **CLI flags** (`--set key=value`)

## Sources

```hocon
sources {
  <source_id> {
    type = postgresql | s3-kinesis

    # PostgreSQL
    connection = "postgresql://user:pass@host:5432/dbname"
    slot_mode = ephemeral | stateful         # default: ephemeral
    slot_name = "laredo_01"                  # required for stateful

    publication {
      name = "laredo_01_pub"                 # default: {slot_name}_pub
      create = false                         # default: false
      publish = "insert, update, delete, truncate"
      tables {
        "schema.table" {
          where = "status = 'active'"        # PG 15+ row filter
          columns = [col1, col2]             # PG 15+ column list
        }
      }
    }

    reconnect {
      max_attempts = 10                      # default: 10
      initial_backoff = 1s                   # default: 1s
      max_backoff = 60s                      # default: 60s
      backoff_multiplier = 2.0               # default: 2.0
    }

    # S3 + Kinesis
    baseline_bucket = "s3://bucket/prefix/"
    baseline_format = jsonl
    stream_arn = "arn:aws:kinesis:..."
    consumer_group = "laredo-01"
    checkpoint_table = "dynamodb://table"
    region = us-east-1
  }
}
```

## Tables & Pipelines

```hocon
tables = [
  {
    source = <source_id>
    schema = public
    table = config_document

    targets = [
      {
        type = indexed-memory | compiled-memory | http-sync | replication-fanout

        # indexed-memory
        lookup_fields = [field1, field2]
        additional_indexes = [
          { name = idx_name, fields = [field], unique = false }
        ]

        # compiled-memory
        compiler = "compiler_name"
        key_fields = [field1, field2]
        filter { field = key, prefix = "prefix/" }

        # http-sync
        base_url = "https://..."
        batch_size = 500                     # default: 500
        flush_interval = 200ms               # default: 200ms
        timeout_ms = 5000                    # default: 5000
        retry_count = 3                      # default: 3
        auth_header = "Bearer ..."
        headers { X-Custom = value }

        # replication-fanout
        journal { max_entries = 1000000, max_age = 24h }
        snapshot { interval = 5m, store = local, retention { keep_count = 5 } }
        grpc { port = 4002, max_clients = 500 }
        client_buffer { max_size = 50000, policy = drop_disconnect }

        # Common
        buffer { max_size = 10000, policy = block }
        error_handling {
          max_retries = 5
          retry_backoff_ms = 1000
          retry_backoff_max_ms = 30000
          on_persistent_failure = isolate    # isolate | stop_source | stop_all
          dead_letter { enabled = false, store = s3, config { ... } }
        }
        filters = [{ type = field-equals, field = f, value = v }]
        transforms = [{ type = drop-fields, fields = [f1, f2] }]
      }
    ]

    ttl { mode = field, field = expires_at, check_interval = 30s }
    validation {
      post_baseline_row_count = true
      periodic_row_count { enabled = true, interval = 1h }
      on_mismatch = warn                     # warn | re_baseline | error
    }
  }
]
```

## Snapshots

```hocon
snapshot {
  enabled = true
  store = s3 | local
  store_config { bucket = ..., prefix = ..., region = ..., path = ... }
  serializer = jsonl
  schedule = "every 6h"
  on_shutdown = true
  retention { keep_count = 10, max_age = 7d }
  user_meta { key = value }
  on_restore_mismatch = warn                 # warn | reject
}
```

## gRPC Server

```hocon
grpc {
  port = 4001
  tls { enabled = false, cert_path = "", key_path = "" }
}
```

## Health

```hocon
health {
  http_port = 8080
  readiness_path = "/health/ready"
  liveness_path = "/health/live"
}
```

## Observability

```hocon
observability {
  metrics { type = prometheus, port = 9090, path = "/metrics" }
  logging { type = structured, level = info }
}
```
