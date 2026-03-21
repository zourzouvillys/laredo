// Package engine contains internal engine sub-components (buffer, ACK tracker,
// readiness tracker, TTL manager, shutdown coordinator). The main engine
// orchestrator lives in the root laredo package to avoid import cycles.
package engine
