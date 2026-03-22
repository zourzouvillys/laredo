---
sidebar_position: 6
title: Environment Variables
---

# Environment Variables Reference

Laredo uses environment variables for configuration overrides, connection secrets, and CLI settings. Environment variables are applied after config files and conf.d directories, but before `--set` CLI flags.

## Resolution order

Config is resolved in this order (later overrides earlier):

1. Built-in defaults
2. Config file (`--config` flag or `LAREDO_CONFIG`)
3. Config directory (`/etc/laredo/conf.d/*.conf`, alphabetical)
4. **Environment variables**
5. CLI flags (`--set key=value`)

## Server environment variables

### `LAREDO_CONFIG`

Path to the HOCON configuration file. Used by `laredo-server` when the `--config` flag is not provided.

```bash
export LAREDO_CONFIG=/etc/laredo/laredo.conf
laredo-server
```

This variable is also used by the `validate` and `config` subcommands:

```bash
export LAREDO_CONFIG=/etc/laredo/laredo.conf
laredo-server validate
laredo-server config
```

If neither `--config` nor `LAREDO_CONFIG` is set, the server exits with an error:

```
config file required: use --config flag or LAREDO_CONFIG env var
```

### HOCON key to env var conversion

Any HOCON config key can be overridden via an environment variable. The conversion rule is:

1. Replace all dots (`.`) with underscores (`_`).
2. Convert to uppercase.
3. Optionally prefix with `LAREDO_`.

The `LAREDO_`-prefixed form takes precedence over the bare form. If both `LAREDO_SOURCES_PG_MAIN_CONNECTION` and `SOURCES_PG_MAIN_CONNECTION` are set, the `LAREDO_` variant wins.

**Pattern:**

```
HOCON path:       sources.pg_main.connection
Bare env var:     SOURCES_PG_MAIN_CONNECTION
Prefixed env var: LAREDO_SOURCES_PG_MAIN_CONNECTION  (takes precedence)
```

### Source config overrides

All source fields can be overridden through environment variables. Given a source ID of `pg_main`:

| HOCON path | Environment variable | Description |
|---|---|---|
| `sources.pg_main.connection` | `LAREDO_SOURCES_PG_MAIN_CONNECTION` | PostgreSQL connection string |
| `sources.pg_main.type` | `LAREDO_SOURCES_PG_MAIN_TYPE` | Source type (`postgresql`, `pg`) |
| `sources.pg_main.slot_mode` | `LAREDO_SOURCES_PG_MAIN_SLOT_MODE` | Slot mode (`ephemeral`, `stateful`) |
| `sources.pg_main.slot_name` | `LAREDO_SOURCES_PG_MAIN_SLOT_NAME` | Replication slot name |

This is the recommended way to inject database credentials in containerized environments:

```bash
export LAREDO_SOURCES_PG_MAIN_CONNECTION="postgresql://user:secret@db:5432/mydb"
laredo-server --config /etc/laredo/laredo.conf
```

### Target config overrides

Target fields within each table are overridden using positional indices (zero-based). Given the first table (`tables.0`) and its first target (`targets.0`):

| HOCON path | Environment variable | Description |
|---|---|---|
| `tables.0.targets.0.base_url` | `LAREDO_TABLES_0_TARGETS_0_BASE_URL` | HTTP sync target base URL |
| `tables.0.targets.0.auth_header` | `LAREDO_TABLES_0_TARGETS_0_AUTH_HEADER` | HTTP sync auth header |

This is useful for injecting target credentials without putting them in config files:

```bash
export LAREDO_TABLES_0_TARGETS_0_AUTH_HEADER="Bearer my-secret-token"
laredo-server --config /etc/laredo/laredo.conf
```

### gRPC port override

| HOCON path | Environment variable | Description |
|---|---|---|
| `grpc.port` | `LAREDO_GRPC_PORT` | gRPC server listen port |

```bash
export LAREDO_GRPC_PORT=4002
laredo-server --config /etc/laredo/laredo.conf
```

## CLI environment variables

### `LAREDO_ADDRESS`

gRPC server address for the `laredo` CLI tool. Overrides the default `localhost:4001`.

```bash
export LAREDO_ADDRESS=laredo.internal:4001
laredo status
```

This is equivalent to using the `--address` flag:

```bash
laredo --address laredo.internal:4001 status
```

## Summary table

| Variable | Used by | Default | Description |
|---|---|---|---|
| `LAREDO_CONFIG` | `laredo-server` | (none) | Path to HOCON config file |
| `LAREDO_ADDRESS` | `laredo` CLI | `localhost:4001` | gRPC server address |
| `LAREDO_SOURCES_<ID>_CONNECTION` | `laredo-server` | (from config) | Source connection string override |
| `LAREDO_SOURCES_<ID>_TYPE` | `laredo-server` | (from config) | Source type override |
| `LAREDO_SOURCES_<ID>_SLOT_MODE` | `laredo-server` | (from config) | Source slot mode override |
| `LAREDO_SOURCES_<ID>_SLOT_NAME` | `laredo-server` | (from config) | Source slot name override |
| `LAREDO_TABLES_<N>_TARGETS_<M>_BASE_URL` | `laredo-server` | (from config) | Target base URL override |
| `LAREDO_TABLES_<N>_TARGETS_<M>_AUTH_HEADER` | `laredo-server` | (from config) | Target auth header override |
| `LAREDO_GRPC_PORT` | `laredo-server` | (from config) | gRPC listen port override |

## Kubernetes example

Use Kubernetes secrets and environment variables to inject credentials without storing them in config files:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: laredo
spec:
  template:
    spec:
      containers:
        - name: laredo-server
          image: ghcr.io/zourzouvillys/laredo-server:latest
          env:
            - name: LAREDO_CONFIG
              value: /etc/laredo/laredo.conf
            - name: LAREDO_SOURCES_PG_MAIN_CONNECTION
              valueFrom:
                secretKeyRef:
                  name: laredo-secrets
                  key: pg-connection
            - name: LAREDO_TABLES_0_TARGETS_0_AUTH_HEADER
              valueFrom:
                secretKeyRef:
                  name: laredo-secrets
                  key: http-auth-header
          volumeMounts:
            - name: config
              mountPath: /etc/laredo
      volumes:
        - name: config
          configMap:
            name: laredo-config
```
