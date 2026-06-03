---
sidebar_position: 9
title: Snapshot Writer Runbook
---

# Snapshot Writer Runbook

Operational procedures for `laredo-snapshotter`. See the
[architecture & design](../design/snapshot-writer.md) for how it works.

:::note Design-phase document
The Snapshot Writer is a **proposed** feature
([EDR-0001](https://github.com/zourzouvillys/laredo/blob/main/docs/edr/0001-snapshot-writer.md)).
This runbook describes the operational model the design commits to; commands and
metric names are finalized as each implementation phase lands.
:::

## Health & readiness

| Probe | Meaning |
|---|---|
| `GET /health/live` | Process is up. |
| `GET /health/ready` | The **initial base snapshot is durable** on all destinations. Use for load-balancer / orchestrator readiness. |
| `GET /status` | Current source position, last snapshot/diff, epoch, buffer depth, manifest head. |

A writer that is live but not ready is still bootstrapping (subscribing, loading
the first snapshot, or writing it). If it never becomes ready, check the
subscription (can it reach `laredo-server`?) and the destinations (can it write?).

## Forcing a snapshot

```bash
curl -XPOST http://snapshotter:8080/snapshot
```

Forces an immediate re-base (new base snapshot, new epoch, diff counters reset).
Use before a schema migration, a consumer cutover, or to shorten a cold-read
chain that has grown long.

## Common incidents

### Manifest CAS keeps failing

**Symptom:** logs show repeated "manifest write conflict / precondition failed";
`snapshotter_manifest_cas_retries_total` climbing.

**Cause:** two writers are pointed at the same table + prefix, or a previous
writer did not shut down cleanly.

**Action:** there must be exactly **one** writer per (table, destination prefix).
Identify the duplicate (the manifest's `updated_at` and the writer `client_id` in
events help) and stop it. The CAS guarantees no corruption — the loser simply
retries — but two writers will fight and double-write artifacts.

### A destination is failing writes

**Symptom:** `snapshotter_destination_errors_total{dest=…}` rising; readiness may
drop if it is the only destination.

**Behavior:** an artifact is durable only once written to **all** destinations, so
a single failing destination stalls the manifest commit. Buffered changes
continue to accumulate in memory (watch `snapshotter_buffer_depth` and process
RSS).

**Action:** restore the destination (bucket policy, credentials, network). The
writer retries with backoff and resumes from its in-memory buffer — no data is
lost as long as the process stays up and the fan-out journal still covers its
position. If memory pressure is a risk during a long outage, remove the broken
destination from config and restart (you can backfill it later by forcing a
snapshot once it returns).

### Credentials expired / access denied

**Symptom:** `AccessDenied` / `ExpiredToken` from S3 or an event sink.

**Action:** credential profiles resolve per action group. Check the specific
profile named by the failing component (an S3 destination vs. a Kinesis sink may
use different roles). For assume-role profiles, verify the trust policy, the
`external_id`, and that the base (ambient) identity may assume the target role.
IRSA/instance-role token rotation is automatic; a persistent failure means a
policy/trust problem, not rotation.

### Cold-read chains are too long

**Symptom:** consumers report many diffs to replay; `snapshotter_diffs_since_snapshot`
high.

**Action:** the re-base thresholds are too loose for this workload. Lower
`snapshot.max_interval`, `snapshot.max_churn_records`/`max_churn_fraction`, or
`snapshot.max_diff_bytes`/`max_diff_fraction`. Conversely, if snapshots are too
frequent (storage cost, CPU), raise them or raise `snapshot.min_interval`.

### Events missing or duplicated

**Expected.** Events are at-least-once and advisory. Consumers must poll the
manifest as the source of truth and tolerate gaps/duplicates. If a sink is down,
`snapshotter_event_errors_total{sink=…}` rises but artifacts and the manifest are
unaffected — pollers still see the new head.

## Capacity

- **Memory:** the writer holds the full table in memory (like any `client/fanout`
  consumer) plus the diff buffer between flushes. Size for table size + peak
  buffered churn.
- **Storage:** base snapshots dominate; retention prunes artifacts older than the
  newest snapshot that precedes them. Multiply by the number of formats emitted.
- **One writer per table.** Scale by running more processes, not by widening one.

## Shutdown

On `SIGTERM`/`SIGINT` the writer flushes a final diff (so buffered changes are
durable) and updates the manifest before exiting. Give it a shutdown grace period
long enough to write to all destinations.
