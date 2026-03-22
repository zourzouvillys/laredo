package fanout

import (
	"sync"
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

// clientRegistry tracks connected fan-out clients.
type clientRegistry struct {
	mu         sync.RWMutex
	clients    map[string]*clientState
	maxClients int
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
		clients:    make(map[string]*clientState),
		maxClients: maxClients,
	}
}

// register adds a client. Returns false if the max clients limit is reached.
func (r *clientRegistry) register(clientID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.maxClients > 0 && len(r.clients) >= r.maxClients {
		return false
	}

	r.clients[clientID] = &clientState{
		id:          clientID,
		connectedAt: time.Now(),
		state:       "catching_up",
		sendCh:      make(chan struct{}),
	}
	return true
}

// unregister removes a client.
func (r *clientRegistry) unregister(clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.clients[clientID]; ok {
		close(cs.sendCh)
		delete(r.clients, clientID)
	}
}

// updateSequence updates a client's current position.
func (r *clientRegistry) updateSequence(clientID string, seq int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.clients[clientID]; ok {
		cs.currentSequence = seq
	}
}

// setState updates a client's state.
func (r *clientRegistry) setState(clientID string, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.clients[clientID]; ok {
		cs.state = state
	}
}

// setBufferDepth updates a client's buffer depth.
func (r *clientRegistry) setBufferDepth(clientID string, depth int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.clients[clientID]; ok {
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

// get returns info about a specific client.
func (r *clientRegistry) get(clientID string) (ClientInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cs, ok := r.clients[clientID]
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
