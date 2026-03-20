---
sidebar_position: 3
title: Dead Letters
---

# Dead Letter Management

When a change fails to apply after all retries, it can be captured in a dead letter store for later inspection and replay.

## Enabling dead letters

```hocon
error_handling {
  max_retries = 5
  on_persistent_failure = isolate
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

## Inspecting dead letters

```bash
laredo dead-letters pg_main:public.audit_log:http-sync
```

Output:

```
PIPELINE: pg_main:public.audit_log:http-sync
PENDING: 47

TIMESTAMP                ACTION   POSITION        ERROR
2026-03-20 12:34:44      INSERT   0/1A2B3C4D      HTTP 503: Service Unavailable
2026-03-20 12:34:44      INSERT   0/1A2B3C50      HTTP 503: Service Unavailable
...
```

## Replaying dead letters

After fixing the downstream issue, replay the dead letters:

```bash
laredo dead-letters replay pg_main:public.audit_log:http-sync
# Replaying 47 dead letters... 45 succeeded, 2 failed.
```

Failed replays remain in the dead letter store.

## Purging dead letters

Remove dead letters that are no longer needed:

```bash
laredo dead-letters purge pg_main:public.audit_log:http-sync
# Purged 2 dead letters.
```

## Monitoring

The `laredo_pipeline_dead_letters_total` counter tracks dead letter writes. Alert on this metric to detect persistent downstream failures.
