// Package laredo provides a library for capturing baseline snapshots from data
// sources and streaming real-time changes through pluggable targets.
//
// The core abstractions are:
//   - [SyncSource]: produces baseline snapshots and change streams from a data source.
//   - [SyncTarget]: consumes baseline rows and change events.
//   - [Engine]: orchestrates pipelines binding sources to targets.
//   - [EngineObserver]: receives structured lifecycle and operational events.
//
// Sources, targets, filters, transforms, and snapshot stores are pluggable.
// The engine manages baseline loading, change streaming, ACK coordination,
// backpressure, error isolation, TTL, and snapshots.
package laredo

// Version is set at build time via ldflags.
var Version = "dev"
