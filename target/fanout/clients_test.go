package fanout

import "testing"

func TestClientRegistry_RegisterUnregister(t *testing.T) {
	r := newClientRegistry(0) // unlimited

	if !r.register("client-1") {
		t.Fatal("expected register to succeed")
	}
	if !r.register("client-2") {
		t.Fatal("expected register to succeed")
	}
	if r.count() != 2 {
		t.Errorf("expected 2 clients, got %d", r.count())
	}

	r.unregister("client-1")
	if r.count() != 1 {
		t.Errorf("expected 1 client after unregister, got %d", r.count())
	}
}

func TestClientRegistry_MaxClients(t *testing.T) {
	r := newClientRegistry(2) // max 2

	if !r.register("c1") {
		t.Fatal("expected register to succeed")
	}
	if !r.register("c2") {
		t.Fatal("expected register to succeed")
	}
	if r.register("c3") {
		t.Error("expected register to fail at max capacity")
	}
	if r.count() != 2 {
		t.Errorf("expected 2 clients, got %d", r.count())
	}
}

func TestClientRegistry_UpdateState(t *testing.T) {
	r := newClientRegistry(0)
	r.register("c1")

	r.updateSequence("c1", 42)
	r.setState("c1", "live")
	r.setBufferDepth("c1", 10)

	info, ok := r.get("c1")
	if !ok {
		t.Fatal("expected to find client")
	}
	if info.CurrentSequence != 42 {
		t.Errorf("expected seq=42, got %d", info.CurrentSequence)
	}
	if info.State != "live" {
		t.Errorf("expected state=live, got %s", info.State)
	}
	if info.BufferDepth != 10 {
		t.Errorf("expected depth=10, got %d", info.BufferDepth)
	}
}

func TestClientRegistry_List(t *testing.T) {
	r := newClientRegistry(0)
	r.register("c1")
	r.register("c2")
	r.setState("c1", "live")
	r.setState("c2", "catching_up")

	clients := r.list()
	if len(clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(clients))
	}
}

func TestClientRegistry_DisconnectAll(t *testing.T) {
	r := newClientRegistry(0)
	r.register("c1")
	r.register("c2")

	r.disconnectAll()
	if r.count() != 0 {
		t.Errorf("expected 0 after disconnect all, got %d", r.count())
	}
}

func TestClientRegistry_GetNotFound(t *testing.T) {
	r := newClientRegistry(0)
	_, ok := r.get("nonexistent")
	if ok {
		t.Error("expected not found")
	}
}
