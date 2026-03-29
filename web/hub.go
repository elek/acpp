package web

import (
	"encoding/json"
	"sync"
)

// Hub manages WebSocket subscribers per session ID.
// Gateway publishes events here; connected WebSocket clients receive them.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*subscriber]struct{} // sessionID -> set of subscribers
}

type subscriber struct {
	ch chan []byte
}

// NewHub creates a new Hub.
func NewHub() *Hub {
	return &Hub{
		subs: make(map[string]map[*subscriber]struct{}),
	}
}

// Subscribe returns a subscriber for the given session ID.
// The caller must call Unsubscribe when done.
func (h *Hub) Subscribe(sessionID string) *subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &subscriber{ch: make(chan []byte, 64)}
	if h.subs[sessionID] == nil {
		h.subs[sessionID] = make(map[*subscriber]struct{})
	}
	h.subs[sessionID][s] = struct{}{}
	return s
}

// Unsubscribe removes a subscriber and closes its channel.
func (h *Hub) Unsubscribe(sessionID string, s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs, ok := h.subs[sessionID]; ok {
		delete(subs, s)
		if len(subs) == 0 {
			delete(h.subs, sessionID)
		}
	}
	close(s.ch)
}

// Publish sends a log event to all subscribers of a session.
// Non-blocking: drops the message if a subscriber's buffer is full.
func (h *Hub) Publish(sessionID string, entry logEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	h.mu.RLock()
	subs := h.subs[sessionID]
	h.mu.RUnlock()

	for s := range subs {
		select {
		case s.ch <- data:
		default:
			// subscriber too slow, drop
		}
	}
}
