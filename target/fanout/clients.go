package fanout

import (
	"sync"
	"sync/atomic"
	"time"
)

// ClientInfo describes a connected replication client.
type ClientInfo struct {
	ID              string
	CurrentSequence int64
	ConnectedAt     time.Time
	State           string // "catching_up", "live", "backpressured"
	BufferDepth     int
}

// ClientSession identifies one registration. It exists because a client id is
// not unique in time: the client's own GoAway handoff deliberately runs two
// streams under the same id while the new one catches up.
//
// Keying the registry by id alone meant the second registration overwrote the
// first's entry — leaking its channel, hiding it from the client count so
// maxClients could be bypassed by reusing an id, and, worst, making the first
// stream's deferred unregister tear down the second's entry and release the
// second's journal pin while it was still streaming a snapshot, so the journal
// could prune entries that stream still needed.
type ClientSession struct {
	id    string
	token uint64
}

// ID returns the client id this session was registered under.
func (s ClientSession) ID() string { return s.id }

// clientRegistry tracks connected fan-out clients. Registrations are keyed by
// token, not id, so concurrent sessions under one id stay independent.
type clientRegistry struct {
	mu         sync.RWMutex
	clients    map[uint64]*clientState
	maxClients int
	nextToken  atomic.Uint64
}

type clientState struct {
	id              string
	currentSequence int64
	connectedAt     time.Time
	state           string
	bufferDepth     int
	sendCh          chan struct{} // closed when client should disconnect
}

func newClientRegistry(maxClients int) *clientRegistry {
	return &clientRegistry{
		clients:    make(map[uint64]*clientState),
		maxClients: maxClients,
	}
}

// register adds a client session. Returns false if the max clients limit is
// reached; the limit now counts sessions, so it cannot be evaded by reusing an
// id.
func (r *clientRegistry) register(clientID string) (ClientSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.maxClients > 0 && len(r.clients) >= r.maxClients {
		return ClientSession{}, false
	}

	token := r.nextToken.Add(1)
	r.clients[token] = &clientState{
		id:          clientID,
		connectedAt: time.Now(),
		state:       "catching_up",
		sendCh:      make(chan struct{}),
	}
	return ClientSession{id: clientID, token: token}, true
}

// unregister removes one session, leaving any other session under the same id
// untouched.
func (r *clientRegistry) unregister(s ClientSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.clients[s.token]; ok {
		close(cs.sendCh)
		delete(r.clients, s.token)
	}
}

// updateSequence updates a client's current position.
func (r *clientRegistry) updateSequence(s ClientSession, seq int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.clients[s.token]; ok {
		cs.currentSequence = seq
	}
}

// setState updates a client's state.
func (r *clientRegistry) setState(s ClientSession, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.clients[s.token]; ok {
		cs.state = state
	}
}

// setBufferDepth updates a client's buffer depth.
func (r *clientRegistry) setBufferDepth(s ClientSession, depth int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.clients[s.token]; ok {
		cs.bufferDepth = depth
	}
}

// count returns the number of connected clients.
func (r *clientRegistry) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.clients)
}

// list returns info about all connected clients.
func (r *clientRegistry) list() []ClientInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ClientInfo, 0, len(r.clients))
	for _, cs := range r.clients {
		result = append(result, ClientInfo{
			ID:              cs.id,
			CurrentSequence: cs.currentSequence,
			ConnectedAt:     cs.connectedAt,
			State:           cs.state,
			BufferDepth:     cs.bufferDepth,
		})
	}
	return result
}

// get returns info about a specific session.
func (r *clientRegistry) get(s ClientSession) (ClientInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cs, ok := r.clients[s.token]
	if !ok {
		return ClientInfo{}, false
	}
	return ClientInfo{
		ID:              cs.id,
		CurrentSequence: cs.currentSequence,
		ConnectedAt:     cs.connectedAt,
		State:           cs.state,
		BufferDepth:     cs.bufferDepth,
	}, true
}

// disconnectAll closes all client channels, signaling them to disconnect.
func (r *clientRegistry) disconnectAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, cs := range r.clients {
		close(cs.sendCh)
		delete(r.clients, id)
	}
}
