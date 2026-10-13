package fanout

import "testing"

func TestClientRegistry_RegisterUnregister(t *testing.T) {
	r := newClientRegistry(0) // unlimited

	s1, ok := r.register("client-1")
	if !ok {
		t.Fatal("expected register to succeed")
	}
	if _, ok := r.register("client-2"); !ok {
		t.Fatal("expected register to succeed")
	}
	if r.count() != 2 {
		t.Errorf("expected 2 clients, got %d", r.count())
	}

	r.unregister(s1)
	if r.count() != 1 {
		t.Errorf("expected 1 client after unregister, got %d", r.count())
	}
}

func TestClientRegistry_MaxClients(t *testing.T) {
	r := newClientRegistry(2) // max 2

	if _, ok := r.register("c1"); !ok {
		t.Fatal("expected register to succeed")
	}
	if _, ok := r.register("c2"); !ok {
		t.Fatal("expected register to succeed")
	}
	if _, ok := r.register("c3"); ok {
		t.Error("expected register to fail at max capacity")
	}
	if r.count() != 2 {
		t.Errorf("expected 2 clients, got %d", r.count())
	}
}

func TestClientRegistry_UpdateState(t *testing.T) {
	r := newClientRegistry(0)
	s1, _ := r.register("c1")

	r.updateSequence(s1, 42)
	r.setState(s1, "live")
	r.setBufferDepth(s1, 10)

	info, ok := r.get(s1)
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
	s1, _ := r.register("c1")
	s2, _ := r.register("c2")
	r.setState(s1, "live")
	r.setState(s2, "catching_up")

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
	_, ok := r.get(ClientSession{id: "nonexistent", token: 9999})
	if ok {
		t.Error("expected not found")
	}
}
