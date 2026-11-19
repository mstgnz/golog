package handlers

import (
	"log"
	"sync"

	"github.com/mstgnz/golog/v2/models"
)

// client is one connected SSE stream.
type client struct {
	send chan models.LogEvent
}

// hub fans log events out to SSE clients. A client's channel is closed only
// by whoever removes it from the map, under the lock, so it is closed once.
type hub struct {
	mu         sync.Mutex
	clients    map[*client]struct{}
	closed     bool
	bufferSize int
}

func newHub(bufferSize int) *hub {
	return &hub{clients: make(map[*client]struct{}), bufferSize: bufferSize}
}

// add registers a new client. It returns false once the hub is closed.
func (h *hub) add() (*client, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	c := &client{send: make(chan models.LogEvent, h.bufferSize)}
	h.clients[c] = struct{}{}
	return c, true
}

// remove unregisters c. Safe to call after the hub already dropped c.
func (h *hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropLocked(c)
}

// broadcast delivers ev to every client. A client whose buffer is full is
// dropped rather than allowed to stall everyone else; its browser reconnects.
func (h *hub) broadcast(ev models.LogEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- ev:
		default:
			log.Printf("golog: dropping a stream client that is not keeping up")
			h.dropLocked(c)
		}
	}
}

// close drops every client and refuses new ones; their handlers return.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for c := range h.clients {
		h.dropLocked(c)
	}
}

func (h *hub) len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *hub) dropLocked(c *client) {
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
}
