---
sidebar_position: 5
title: Health Endpoints
---

# Health Endpoints

The pre-built service exposes HTTP health endpoints for container orchestration.

## Configuration

```hocon
health {
  http_port = 8080
  readiness_path = "/health/ready"
  liveness_path = "/health/live"
}
```

## Endpoints

### `/health/live` — Liveness

Returns 200 when the process is running and not deadlocked. Use as a Kubernetes liveness probe.

A pipeline in ERROR state does **not** affect liveness (the process is still healthy). If **all** pipelines on a source error out, liveness fails.

### `/health/ready` — Readiness

Returns 200 when all pipelines have completed baseline and are streaming. Use as a Kubernetes readiness probe.

Returns a JSON body with per-pipeline status:

```json
{
  "ready": false,
  "pipelines": {
    "pg_main:public.config_document:indexed-memory": "STREAMING",
    "pg_main:public.config_document:http-sync": "STREAMING",
    "pg_main:public.audit_log:http-sync": "ERROR: HTTP 503 after 5 retries"
  }
}
```

### `/health/startup` — Startup

Returns 200 when the service has initialized and begun baseline (may not be complete). Use as a Kubernetes startup probe with a generous `failureThreshold` to allow time for large baselines.

## Kubernetes probe configuration

```yaml
livenessProbe:
  httpGet:
    path: /health/live
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 10
readinessProbe:
  httpGet:
    path: /health/ready
    port: 8080
  initialDelaySeconds: 10
  periodSeconds: 5
startupProbe:
  httpGet:
    path: /health/live
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 5
  failureThreshold: 60    # 5 minutes for initial baseline
```
