---
id: 7
title: "Authenticated fan-out: an authorizer that imposes scope, and clients that acknowledge what they applied"
status: proposed
proposed_until: 2026-11-22
date: 2026-08-24
authors:
  - "Theo Zourzouvillys <theo@zrz.io>"
tags: [replication, fanout, security, authorization, protocol]
supersedes: null
superseded_by: null
aliases: []
---

## TL;DR

The replication fan-out gains two things it could not be deployed on a shared network without.

**An `Authorizer`**, consulted for every request on every service, which returns *predicates the
server ANDs into a subscription* rather than approving the ones the client sent, and which decides
the client id. Without one configured the server behaves as before and authorizes nothing.

**A bidirectional `Sync`**, so a client can report the position it has actually applied.
`GetReplicationStatus` now distinguishes what was *sent* to a subscriber from what that subscriber
*installed*. This is a breaking protocol change: `SyncRequest` becomes the first message of a
client stream, `SyncStart`.

Alongside them, this record covers the correctness and resource defects fixed in the same release,
because several of them are the reason the fan-out could not be trusted with configuration data.

## Context

The fan-out was built to distribute a table to many readers, and it does that well. Putting it in
front of *configuration* — where a subscriber's copy decides how a service behaves — asked two
questions of it that had not been asked before: who may read this table, and how do I know a
change arrived?

Neither had an answer.

**Nothing authenticated anything.** `service.New` registered Connect handlers on an unexported mux
with no handler options and no interceptor seam, so there was no way to add authentication from
outside the package either. Every RPC was reachable by anything that could open a socket. That
includes OAM's `ResetSource`, which drops and recreates a replication slot; `DrainReplication`,
which with an empty schema and table drains every fan-out target in the engine and carries no
confirmation flag at all; and Query's `Subscribe`, which streams an entire table, live, to any
caller.

**`GetReplicationStatus` could only report what had been sent.** `ConnectedClient.current_sequence`
is the server's own send-side bookkeeping. A subscriber that received a row and then failed to
decode it, or deadlocked applying it, was indistinguishable from one that had it. Anything built on
that status to answer "has this change reached the fleet?" was answering a different question,
confidently.

And a set of defects underneath both, each of which produced a replica that quietly disagreed with
its source rather than an error:

- `structpb.NewStruct` rejects `time.Time`, `[16]byte`, `pgtype.Numeric` and `netip.Prefix` — all
  of which the baseline path yields — and the error was discarded at eight call sites. The nil
  Struct went on the wire, the client skipped the row, and the journal sequence advanced anyway.
  Any table with a `timestamptz`, `uuid` or `numeric` column lost rows on the snapshot path, with
  nothing in any log.
- The same column arrived as a native Go type on the baseline path and as raw text on the streaming
  path, so a row's type depended on how the process learned about it and numeric subscription
  filters matched during catch-up but not afterwards.
- The client held its write lock across the change listener, so a listener that read the client
  back — the natural thing for a consumer maintaining derived state — deadlocked the stream
  goroutine permanently.
- The client keyed rows on a column literally named `id`, while the server keyed them on the
  declared primary key.
- `SnapshotBegin` cleared the store in place while `ready` stayed true, so a re-snapshot let
  readers observe the replica empty and then refill.
- Client sessions were keyed by client id, but the GoAway handoff deliberately runs two streams
  under one id, so the second displaced the first and the first's teardown released the second's
  journal pin mid-snapshot.
- Snapshot retention and the client cap defaulted to unlimited, and every full sync takes a
  snapshot holding a complete copy of the table.

## Decision

### The Authorizer imposes scope

`service.Authorizer` receives the procedure, the request headers, and — for `Sync`, on a second
call once the client's opening message has been read — the schema, table and requested filters. It
returns a subject and a set of predicates.

The predicates are **added** to the subscription, not checked against it. This is the whole design.
A client's filters are subtractive: sending none asks for the entire table, so validating what was
asked for cannot constrain a caller who asks for everything. Imposing predicates can, and that is
what allows a subscriber to be pinned server-side to its own partition.

The subject becomes the client id. That field was previously a free-form unauthenticated string,
and the status view is keyed on it, so a caller could present itself as another subscriber.

`Sync` authorizes twice because its target arrives in the first message rather than in metadata.
`FetchSnapshot` resolves its snapshot to the owning table before sending a row: it takes only a
snapshot id, applies no subscription filter, and ids are formatted
`fanout-<journalSeq>-<unixMillis>` from components the status and handshake messages disclose.

A server with no `Authorizer` allows everything, exactly as before. The library stays usable
without auth; a deployment that opts in does not silently leave a door open.

`service/auth/oidc` ships as a discovery+JWKS implementation. It requires an explicit `Authorize`
callback rather than defaulting to allow, because a verified token establishes who is calling, not
what they may reach.

### Sync is bidirectional

The client opens with a `SyncStart` and may then send `ApplyAck` messages carrying its applied
sequence, applied source position, an opaque generation for confirming two subscribers hold the
same state, and an apply error when it cannot install what it received. All four appear on
`ConnectedClient` beside the sent-side fields, so the two are visibly distinct.

Three consequences followed:

- The transport is unencrypted HTTP/2 via the standard library's `Protocols`, because Connect
  carries bidirectional streams over HTTP/2 only and a plaintext listener otherwise negotiates
  HTTP/1.1.
- The Go client dials with the **gRPC** protocol. Connect's own streaming protocol is half-duplex:
  the server may not send until the client has finished sending, and a client holding the stream
  open to acknowledge never finishes.
- `Receive` on a bidirectional stream does not observe context cancellation, so the client closes
  the stream when its context ends.

## Consequences

**Breaking.** `Sync(SyncRequest) returns (stream SyncResponse)` becomes
`Sync(stream SyncClientMessage) returns (stream SyncResponse)`. A v0.3.0 client cannot talk to a
v0.4.0 server. The library is pre-1.0 and the fan-out has no known external consumers, so the
protocol is broken now rather than carried.

**Plaintext deployments must speak HTTP/2.** Anything fronting the fan-out — a proxy, a load
balancer — has to support h2c or terminate TLS with HTTP/2 negotiated.

**An acknowledgement is a claim, not proof.** A client reports what it believes it applied. This is
a large improvement on send-side status, which could not even be wrong about the client, but it is
still the client's own account of itself. A subscriber that lies, or that acknowledges before
installing, is not detected.

**Bounded defaults may reject work that previously succeeded.** Snapshot retention, the client cap,
the filter predicate count and `in` list length, and the request body size all now have limits.
They are set where a real deployment should not notice, but they are limits where there were none.

**An Authorizer sees every request.** It is on the path of each RPC, including the per-request
authorization for `Sync`, so an implementation that makes a network call per request will be felt.
The OIDC implementation caches keys and verifies locally for this reason.

**`WithTLS` now fails loudly.** It previously swallowed a certificate load error and started in
plaintext; `Start` returns the error instead. A deployment with a wrong certificate path that had
been running unnoticed in plaintext will stop starting.

## References

- Fan-out design: `target/fanout/CLAUDE.md`
- Related EDRs: `EDR-0002` (cold-tier replay), `EDR-0004` (cascading fan-out source).

## Changelog

- **2026-08-24**: Proposed, for feedback until 2026-11-22.
