package fanout

import (
	"testing"

	"github.com/zourzouvillys/laredo"
)

// TestClientRegistry_ConcurrentSessionsUnderOneID is the regression test for
// the id-aliasing bug. The client's own GoAway handoff runs two streams under
// one client id on purpose, and the registry keyed entries by id — so the
// second registration silently displaced the first, and the first stream's
// deferred unregister then tore down the second's still-live entry.
func TestClientRegistry_ConcurrentSessionsUnderOneID(t *testing.T) {
	r := newClientRegistry(0)

	first, ok := r.register("warden-1")
	if !ok {
		t.Fatal("first register failed")
	}
	second, ok := r.register("warden-1")
	if !ok {
		t.Fatal("second register under the same id failed")
	}

	if r.count() != 2 {
		t.Fatalf("count = %d, want 2 — the second registration displaced the first", r.count())
	}

	// The handoff ends: the old stream unregisters. The new one must survive.
	r.unregister(first)

	if r.count() != 1 {
		t.Fatalf("count = %d, want 1 after the first session ended", r.count())
	}
	if _, ok := r.get(second); !ok {
		t.Fatal("the surviving session was removed by the other session's unregister")
	}

	// Updates to the live session must still land.
	r.updateSequence(second, 99)
	info, ok := r.get(second)
	if !ok {
		t.Fatal("live session vanished")
	}
	if info.CurrentSequence != 99 {
		t.Errorf("CurrentSequence = %d, want 99 — updates to the live session are no-ops",
			info.CurrentSequence)
	}
}

// TestClientRegistry_MaxClientsCountsSessions covers the cap bypass: because
// re-registering an id replaced the map entry rather than adding one, len()
// never grew and any number of connections could share one slot.
func TestClientRegistry_MaxClientsCountsSessions(t *testing.T) {
	r := newClientRegistry(2)

	if _, ok := r.register("same"); !ok {
		t.Fatal("first register failed")
	}
	if _, ok := r.register("same"); !ok {
		t.Fatal("second register failed")
	}
	if _, ok := r.register("same"); ok {
		t.Error("a third session under one id was admitted; maxClients is bypassable by id reuse")
	}
	if r.count() != 2 {
		t.Errorf("count = %d, want 2", r.count())
	}
}

// TestJournalPins_AreScopedToASession covers the consequence that mattered
// most: pins were keyed by client id, so the first stream's deferred unpin
// released the second stream's pin while it was still sending a snapshot, and
// the journal could then prune entries that stream still needed.
func TestJournalPins_AreScopedToASession(t *testing.T) {
	j := newJournal(1000, 0)
	for i := 0; i < 10; i++ {
		j.append(laredo.ActionInsert, nil, nil, nil)
	}

	first := ClientSession{id: "warden-1", token: 1}
	second := ClientSession{id: "warden-1", token: 2}

	j.pin(first.token, 3)
	j.pin(second.token, 5)

	j.unpin(first.token)

	j.mu.RLock()
	_, stillPinned := j.pins[second.token]
	j.mu.RUnlock()
	if !stillPinned {
		t.Fatal("the surviving session's journal pin was released by the other session's unpin")
	}
}
