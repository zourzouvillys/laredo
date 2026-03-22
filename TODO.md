# Laredo — Remaining Work

Items not yet implemented. Everything else from the original v1.0 roadmap has been completed and shipped in v0.1.0.

---

## PostgreSQL Source

- [x] Sync publication on startup (add/remove tables, update publish operations)
- [x] Row filters and column lists (PostgreSQL 15+)
- [x] Ephemeral mode reconnect: signal engine for full re-baseline

## Fan-Out Replication Protocol

- [x] Delta-from-snapshot mode: tell client to use local snapshot, send journal delta → live
- [ ] Atomic handoff: pin journal during snapshot send, no gaps
