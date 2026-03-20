---
sidebar_position: 3
title: HTTP Sync
---

# HTTP Sync Target

Forward every row change to a downstream HTTP service as batched POST requests.

## Configuration

```hocon
targets = [{
  type = http-sync
  base_url = "https://downstream.example.com/api/sync"
  batch_size = 500
  flush_interval = 200ms
  timeout_ms = 5000
  retry_count = 3
  retry_backoff_ms = 500
  auth_header = "Bearer ..."
  headers {
    X-Source = laredo
  }
}]
```

## HTTP protocol

### Baseline

```
POST {base_url}/baseline/start    → {"table": {...}, "columns": [...]}
POST {base_url}/baseline/batch    → {"table": {...}, "rows": [{...}, ...]}  (repeated)
POST {base_url}/baseline/complete → {"table": {...}}
```

### Changes

```
POST {base_url}/changes → {
  "table": {"schema": "public", "table": "config_document"},
  "changes": [
    {"action": "INSERT", "lsn": "0/1A2B3C4D", "columns": {...}},
    {"action": "UPDATE", "lsn": "0/1A2B3C50", "columns": {...}, "identity": {...}},
    ...
  ]
}
```

## Batching

Changes are buffered internally and flushed when either condition is met:

- Buffer reaches `batch_size`
- `flush_interval` elapses

This batching is transparent to the ACK system — `IsDurable()` returns `true` only after a batch is flushed and a 2xx response is received. Buffered-but-unflushed changes are **not** considered durable.

## Retry

On HTTP failure, the target retries with exponential backoff up to `retry_count` times. If all retries are exhausted, the error propagates to the pipeline error policy.

## Library usage

```go
target := httpsync.New(
    httpsync.BaseURL("https://downstream.example.com/api/sync"),
    httpsync.BatchSize(500),
    httpsync.FlushInterval(200 * time.Millisecond),
    httpsync.Timeout(5 * time.Second),
    httpsync.AuthHeader("Bearer ..."),
)
```
