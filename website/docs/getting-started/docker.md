---
sidebar_position: 3
title: Docker
---

# Docker

Run Laredo with Docker for quick setup and Kubernetes-ready deployments.

## Docker Compose

Create a `docker-compose.yml` to run Laredo alongside PostgreSQL:

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: laredo
      POSTGRES_PASSWORD: laredo
      POSTGRES_DB: laredo
    command: >
      postgres
        -c wal_level=logical
        -c max_replication_slots=4
        -c max_wal_senders=4
    ports:
      - "5432:5432"

  laredo:
    image: ghcr.io/zourzouvillys/laredo-server:latest
    ports:
      - "4001:4001"   # gRPC (OAM + Query)
      - "8080:8080"   # Health
      - "9090:9090"   # Metrics
    volumes:
      - ./laredo.conf:/etc/laredo/laredo.conf
    depends_on:
      - postgres
```

## Configuration

Mount your config file or use environment variables:

```bash
# Environment variable override
docker run \
  -e LAREDO_SOURCES_PG_MAIN_TYPE=postgresql \
  -e LAREDO_SOURCES_PG_MAIN_CONNECTION="postgresql://laredo:laredo@postgres:5432/laredo" \
  -e LAREDO_SOURCES_PG_MAIN_SLOT_MODE=ephemeral \
  ghcr.io/zourzouvillys/laredo-server:latest
```

## Health checks

The image exposes health endpoints for container orchestration:

| Endpoint | Purpose | Returns 200 when |
|---|---|---|
| `/health/live` | Liveness | Process is running |
| `/health/ready` | Readiness | All pipelines are streaming |
| `/health/startup` | Startup | Service has begun baseline |

## Building a custom image

```dockerfile
FROM ghcr.io/zourzouvillys/laredo-server:latest
COPY laredo.conf /etc/laredo/laredo.conf
```

## Volumes

| Path | Purpose |
|---|---|
| `/etc/laredo/` | Config file(s) |
| `/etc/laredo/conf.d/` | Additional config fragments |
| `/var/lib/laredo/` | Local snapshots, dead letters |

## Next steps

- [Kubernetes](/guides/kubernetes) for production deployment with health probes
- [Configuration Reference](/reference/configuration) for all config options
