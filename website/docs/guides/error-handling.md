---
sidebar_position: 7
title: Error Handling
---

# Error Handling

Each pipeline has independent error handling. A failure in one pipeline does not affect others.

## Error policies

```hocon
error_handling {
  max_retries = 5
  retry_backoff_ms = 1000
  retry_backoff_max_ms = 30000
  on_persistent_failure = isolate
}
```

| Policy | Behavior |
|---|---|
| `isolate` | Mark pipeline as ERROR, continue all others. Source ACK advances past this pipeline. |
| `stop_source` | Stop all pipelines on this source. Other sources continue. |
| `stop_all` | Halt the engine. |

## Retry

On a transient error (e.g., HTTP 503), the engine retries the change with exponential backoff:

1. Wait `retry_backoff_ms`
2. Retry the change
3. If still failing, double the backoff (up to `retry_backoff_max_ms`)
4. After `max_retries`, apply the error policy

## Dead letter store

Failed changes can be captured in a dead letter store for later inspection and replay:

```hocon
error_handling {
  dead_letter {
    enabled = true
    store = s3
    config {
      bucket = my-bucket
      prefix = "dead-letter/"
    }
  }
}
```

### CLI commands

```bash
# List dead letters for a pipeline
laredo dead-letters pg_main:public.audit_log:http-sync

# Replay dead letters (re-deliver to the target)
laredo dead-letters replay pg_main:public.audit_log:http-sync

# Purge dead letters
laredo dead-letters purge pg_main:public.audit_log:http-sync
```

## TTL / expiry

In-memory targets can have automatic row expiry based on a timestamp column:

```hocon
ttl {
  mode = field
  field = expires_at
  check_interval = 30s
}
```

Expired rows are removed from the target on the check interval. The engine fires `OnRowExpired` through the observer for each removal.
