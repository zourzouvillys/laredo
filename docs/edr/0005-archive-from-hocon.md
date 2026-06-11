---
id: 5
title: "Cold-tier archive from HOCON: register a fan-out's archive reader in laredo-server config"
status: accepted
date: 2026-06-11
authors:
  - "Theo Zourzouvillys <theo@zrz.io>"
tags: [fan-out, replication, snapshot, archive, config, hocon, laredo-server, s3, onboard]
supersedes: null
superseded_by: null
aliases: []
proposed_until: 2026-09-11
---

## TL;DR

[EDR-0002](/edr/0002-cold-tier-replay) gave the replication service a read-only
**cold archive** seam — `replication.WithArchive(schema, table, reader)` — and
[ADR-007](/internals/adrs#adr-007-server-side-fan-out-wiring) made the stock
`laredo-server` serve a fan-out from HOCON. The one thing still missing is the
bridge between them: there is **no way to register an archive from config**, so
cold-tier replay only works in a hand-written `main`. EDR-0002 named this gap
explicitly ("a later change wires this from HOCON … the engine-level option above
is the seam").

This EDR closes it. A `replication-fanout` target gains an optional `archive`
block; `laredo-server` reads it, builds a `snapshotter.Reader` over the same
destination + prefix + format the snapshotter wrote, and registers it via
`WithArchive` for the target's `(schema, table)`. No new replication, reader, or
wire code — pure config → reader → existing seam.

We ship **local archives first**. Local destinations have zero external deps and
are exactly the offline-first case cold replay was built for — an onboard node
that archives to local disk (CLAUDE.md §5.9) and resumes from it after days dark.
**S3 is deferred** to a follow-up, because doing it right means *extracting the
snapshotter binary's destination/credential builder into a shared package* rather
than duplicating AWS assume-role logic into the config path. The HOCON schema is
designed so S3 slots in as another `store` value with no contract change.

> **Update (S3 shipped).** The deferred follow-up has since landed: the shared
> builder was extracted as `snapshotter/destwire`, the snapshotter binary now
> delegates to it, and `laredo-server` accepts `archive.store = s3` (ambient AWS
> credentials). The "deferred / rejected" statements below record the original
> phasing; both `local` and `s3` are now wired. Named profiles / assume-role for
> the server archive remain future work. See the Changelog.

## Context

Three things are already true, and they line up into a one-block gap:

- **The reader exists and is engine-level.** `snapshotter.NewReader(dest,
  keyPrefix, formats...)` returns a read-only `*Reader`; `replication.New(engine,
  replication.WithArchive("public", "events", reader))` registers it per table
  (EDR-0002). Cold replay then bridges archive→hot-journ→live, falling back to a
  live full snapshot whenever the archive cannot cover the gap. The replication
  service already resolves the owning `SyncSource` and reuses its position
  comparator, so registration is the *only* missing input.
- **The server reads HOCON.** ADR-007 added the `replication-fanout` target type
  and mounts one engine-global replication service. The place a per-table archive
  belongs — inside a fan-out target's config — now exists.
- **The snapshotter already turns config into destinations.** `laredo-snapshotter`
  parses a `destinations` array (`type = local|s3`, `path` / `bucket` / `prefix`
  / `region` / `credentials`) and wires it to `local.New` / `s3.New` through
  `buildDestinations` + an `awsConfigCache` (assume-role aware). But that code
  lives in **`cmd/laredo-snapshotter` — a `main` package** that nothing else can
  import.

So the read side, the server, and the destination-building logic all exist; they
are just not connected, and the one piece that *is* reusable (destination
building) is locked inside a binary.

The forces that shape the design:

- **The archive is per `(schema, table)`.** `WithArchive` keys on it, and a
  `replication-fanout` target *is* per-table (it lives in a table block). The
  archive config belongs on the target; schema/table come from the enclosing
  table. There is no top-level archive registry to invent.
- **`key_prefix` is a contract with the writer.** The reader's manifest/artifact
  keys are derived from the same prefix the snapshotter `Writer` used. There is no
  safe default that always matches, so the prefix is operator-supplied and its
  "must match" nature is the central correctness note (the same coupling EDR-0002
  already documents for the library).
- **S3 needs credentials done once, not twice.** Re-implementing assume-role /
  profile resolution in the config package would create a second place to
  maintain AWS auth — exactly the kind of drift the project rejects. The clean
  move is to *extract* the snapshotter's builder, not copy it. That extraction is
  worth its own change; it should not gate local archives, which need none of it.
- **Cold replay is an optimization, never a dependency.** Absent or broken
  archive ⇒ live full snapshot (EDR-0002's load-bearing fallback). Config wiring
  must preserve that: a misconfigured `archive` block degrades to today's
  behaviour with a logged warning, it never fails the server or silently drops
  data.

## Decision

### 1. An `archive` block on the `replication-fanout` target

```hocon
tables = [{
  source = pg
  schema = public
  table  = events

  targets = [{
    type = replication-fanout
    journal { max_entries = 1000000, max_age = 24h }

    # NEW — optional. Absent ⇒ exactly today's behaviour (too-stale → live snapshot).
    archive {
      store = local                                   # local | s3 (s3 deferred — see below)
      store_config { path = "/var/lib/laredo/archive/events" }
      format     = jsonl                              # jsonl | protobuf; default jsonl; may be a list
      key_prefix = "public.events/"                   # MUST match the snapshotter Writer's KeyPrefix
    }
  }]
}]
```

- `store` / `store_config` mirror the snapshotter's `destinations` shape, so an
  operator who already runs the snapshotter recognises it. `local` uses
  `store_config.path`; `s3` (future) uses `bucket` / `prefix` / `region` /
  `credentials`.
- `format` selects the decoder(s). Default `jsonl`. A list is accepted and the
  reader tries them in order (the `Reader` already supports multiple formats),
  so an archive written in either format stays readable.
- `key_prefix` is the per-table prefix the snapshotter wrote under. It is
  operator-supplied because only the operator knows what the writer used; a
  wrong prefix surfaces as "manifest not found" and a logged fall-back to the
  live path, never as corrupt data.

`laredo-server`, after building the engine and before constructing the
replication service, walks each table's `replication-fanout` target; for every
one with an `archive` block it builds the reader and accumulates a
`replication.WithArchive(table.schema, table.table, reader)` option, then passes
them all to `replication.New(eng, …)`.

### 2. Local first; S3 via a shared destination-builder (deferred)

This PR wires `store = local` end-to-end: `local.New(path)` → `format(s)` →
`snapshotter.NewReader` → `WithArchive`. `store = s3` parses and validates but is
rejected at build time with a clear "not yet supported in laredo-server; use a
local archive or the snapshotter for S3" message — *not* silently ignored.

S3 lands in a follow-up that first **extracts** `cmd/laredo-snapshotter`'s
`buildDestinations` + `buildFormats` + `awsConfigCache` into an importable
package (working name `snapshotter/destwire`), so both the snapshotter binary and
`laredo-server` build destinations and resolve credentials through one code path.
The HOCON schema above is the final shape either way: enabling S3 changes which
`store` values build, not the contract.

### 3. Reuse only — no new replication/reader surface

Nothing in `service/replication` or `snapshotter/reader.go` changes. The reader is
read-only; the snapshotter remains the sole writer. The position comparator, the
hot-journal pin, the gapless handoff, and the fallback are all EDR-0002 code,
reached unchanged through the registered reader.

## Scope — in

- An `archive` block on the `replication-fanout` target type, parsed in
  `laredo-server`'s config (`store`, `store_config`, `format`, `key_prefix`).
- `store = local` wired end-to-end: build `*snapshotter.Reader` and register it
  via `replication.WithArchive` for the target's `(schema, table)`.
- Validation: `store = s3` is rejected with a clear message; a malformed/missing
  archive degrades to the live path with a logged warning, never a hard failure.
- Tests: config parse (local archive → reader built and registered) and a
  server-level check that a configured archive enables `SYNC_MODE_REPLAY_ARCHIVE`
  (reusing the EDR-0002 cold-replay harness), plus the absent-archive no-op case.
- Docs: the fan-out guide's "Cold-tier replay → Enabling it" gains the HOCON form
  next to the Go form; the configuration reference gains the `archive` block;
  CHANGELOG + TODO.

## Scope — out

- _(Originally out, since shipped — see the Update above.)_ **S3 archives in
  `laredo-server`** and **the `snapshotter/destwire` extraction**: both landed.
  Recorded here as the original phasing.
- **Named profiles / assume-role for the server archive.** S3 uses the ambient
  credential chain; profile-based credentials (which the snapshotter supports via
  its config) are not wired into the `laredo-server` archive block. Future work.
- **New `Format` / `Destination` backends**, S3-compatible endpoints (MinIO/R2),
  or credential schemes beyond what the snapshotter already supports — out of
  scope.
- **Durable on-disk *fan-out* snapshots.** Separate concern (the in-memory
  snapshot retention from ADR-007); unrelated to the cold archive.
- **Any change to cold-replay semantics.** Mode selection, fallback, and handoff
  are EDR-0002 and stay as-is.

## Consequences

**Easier:**

- **Cold-tier replay works on the stock binary.** An onboard or regional node
  reconnecting after a long absence resumes from its local archive with the
  history it missed — configured, not coded.
- **One recognisable schema.** Operators who run the snapshotter already write
  `store` / `store_config`; the archive block reuses that vocabulary.
- **S3 is a small additive step, not a redesign.** The schema and registration
  path are final; only the destination-builder needs a home.

**Harder:**

- **`key_prefix` is load-bearing config.** A mismatch silently yields "no archive
  coverage" → live snapshot. It is observable (a logged decline + the existing
  cold-replay metrics), but it is a value operators must get right, and the docs
  must say so plainly.
- **A second config surface couples to the snapshotter's layout.** Like EDR-0002
  coupled the *code* to the manifest, this couples *config* to the writer's
  destination + prefix. Both move together; the docs cross-link the two.

**New obligations:**

- **`store = s3` must fail loudly, not silently.** Until the shared builder lands,
  an S3 archive block is a configuration error with a clear message — never a
  no-op that looks like it worked.
- **Degrade-don't-die stays invariant.** A bad archive block reduces to today's
  behaviour with a warning; it never prevents the server from starting or serving
  the hot path.

## References

- [EDR-0002 — Cold-tier replay](/edr/0002-cold-tier-replay) — the reader, the
  `WithArchive` seam, and the fallback this EDR configures.
- [EDR-0001 — Snapshot writer](/edr/0001-snapshot-writer) — the archive layout,
  `KeyPrefix`, and the `destinations` config this schema mirrors.
- [ADR-007 — Server-side fan-out wiring](/internals/adrs#adr-007-server-side-fan-out-wiring)
  — the `replication-fanout` target and engine-global service this extends.
- [Fan-out guide](/guides/fan-out) — cold-tier replay and its enabling steps.
- `cmd/laredo-snapshotter/build.go` — `buildDestinations` / `buildFormats` /
  `awsConfigCache`, the code the S3 follow-up extracts.
- `snapshotter/reader.go`, `service/replication/replication.go` — `NewReader`,
  `WithArchive`, reused unchanged.

## Changelog

- **2026-06-11**: Proposed.
- **2026-06-11**: Accepted; implemented (local archives). A `replication-fanout`
  target's `archive` block is parsed by `laredo-server`, built into a
  `snapshotter.Reader` via `config.BuildArchiveReader`, and registered with
  `replication.WithArchive` per `(schema, table)`. `store = s3` is rejected with
  a clear error pending the shared destination-builder extraction.
- **2026-06-11**: S3 shipped. Extracted `cmd/laredo-snapshotter`'s
  destination/format building into the importable `snapshotter/destwire` package
  (`BuildDestination`, `BuildFormats`, `AmbientAWSConfig`); the snapshotter binary
  now delegates to it. `laredo-server` accepts `archive.store = s3` (ambient AWS
  credentials) through the same package — no duplicated wiring. Named profiles /
  assume-role for the server archive remain future work.
