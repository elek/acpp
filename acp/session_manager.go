package acp

import (
	"sync"
)

// SessionManager is a registry for session lifecycle management.
// It creates, stores, retrieves, and closes sessions.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]Session // sessionID -> Session
}

// NewSessionManager creates a new SessionManager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]Session),
	}
}

// Create starts a new session with the given ID and options, registers it, and returns it.
func (sm *SessionManager) Create(id string, opts SessionOpts) (Session, error) {
	sess := NewSession(id, opts)
	if err := sess.Start(); err != nil {
		return nil, err
	}
	sm.mu.Lock()
	sm.sessions[id] = sess
	sm.mu.Unlock()
	return sess, nil
}

// Get returns a session by ID, or nil if not found.
func (sm *SessionManager) Get(id string) Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessions[id]
}

// Close closes and removes a session by ID.
func (sm *SessionManager) Close(id string) {
	sm.mu.Lock()
	sess, ok := sm.sessions[id]
	if ok {
		delete(sm.sessions, id)
	}
	sm.mu.Unlock()
	if ok {
		sess.Close()
	}
}

// All returns a snapshot of all active sessions.
func (sm *SessionManager) All() map[string]Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make(map[string]Session, len(sm.sessions))
	for k, v := range sm.sessions {
		result[k] = v
	}
	return result
}
