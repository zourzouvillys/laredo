---
sidebar_position: 9
title: Kubernetes
---

# Kubernetes Deployment

Deploy Laredo as a single-replica Deployment with ConfigMap, Secrets, and health probes.

## Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: laredo
spec:
  replicas: 1
  selector:
    matchLabels:
      app: laredo
  template:
    metadata:
      labels:
        app: laredo
    spec:
      containers:
        - name: laredo
          image: ghcr.io/zourzouvillys/laredo-server:latest
          ports:
            - name: grpc
              containerPort: 4001
            - name: health
              containerPort: 8080
            - name: metrics
              containerPort: 9090
          env:
            - name: POSTGRES_URL
              valueFrom:
                secretKeyRef:
                  name: laredo-secrets
                  key: postgres-url
          volumeMounts:
            - name: config
              mountPath: /etc/laredo
            - name: data
              mountPath: /var/lib/laredo
          livenessProbe:
            httpGet:
              path: /health/live
              port: health
            initialDelaySeconds: 5
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /health/ready
              port: health
            initialDelaySeconds: 10
            periodSeconds: 5
          startupProbe:
            httpGet:
              path: /health/live
              port: health
            initialDelaySeconds: 5
            periodSeconds: 5
            failureThreshold: 60
      volumes:
        - name: config
          configMap:
            name: laredo-config
        - name: data
          persistentVolumeClaim:
            claimName: laredo-data
```

## ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: laredo-config
data:
  laredo.conf: |
    sources {
      pg_main {
        type = postgresql
        connection = ${POSTGRES_URL}
        slot_mode = stateful
        slot_name = laredo_01
        publication { create = true }
      }
    }
    tables = [
      {
        source = pg_main
        schema = public
        table = config_document
        targets = [
          { type = indexed-memory, lookup_fields = [instance_id, key] }
        ]
      }
    ]
```

## Health probes

| Probe | Endpoint | Purpose |
|---|---|---|
| Liveness | `/health/live` | Process is running and not deadlocked |
| Readiness | `/health/ready` | All pipelines are streaming |
| Startup | `/health/live` | Service has initialized (60 retries = 5 min for baseline) |

The startup probe's `failureThreshold: 60` gives up to 5 minutes for the initial baseline to complete before Kubernetes considers the pod failed.

## Graceful shutdown

On `SIGTERM`, Laredo:

1. Stops accepting new gRPC connections
2. Drains in-flight requests
3. Takes a snapshot (if configured)
4. Pauses sources and flushes buffers
5. ACKs final positions and closes connections

Set `terminationGracePeriodSeconds` to allow enough time for the shutdown snapshot.
