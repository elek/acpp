package discord

import (
	"log"
	"sync"
	"time"
)

// TypingFunc is the function signature for sending a typing indicator.
type TypingFunc func(channelID string) error

// TypingIndicator manages continuous typing indicators for Discord channels.
// Discord typing indicators expire after ~10 seconds, so this struct handles
// periodic refreshes and re-triggering after messages are sent. Sources are
// Discord channel IDs.
type TypingIndicator struct {
	typingFunc TypingFunc
	stops      map[string]chan struct{}
	mu         sync.Mutex
}

// NewTypingIndicator creates a new TypingIndicator with the given typing function.
func NewTypingIndicator(typingFunc TypingFunc) *TypingIndicator {
	return &TypingIndicator{
		typingFunc: typingFunc,
		stops:      make(map[string]chan struct{}),
	}
}

// Start begins showing the typing indicator for the given source channel.
// If already typing for this source, it restarts the indicator.
func (t *TypingIndicator) Start(source string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Stop any existing typing goroutine for this source
	if stop, exists := t.stops[source]; exists {
		close(stop)
	}

	stop := make(chan struct{})
	t.stops[source] = stop

	go func() {
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()

		// Send initial typing indicator
		if err := t.typingFunc(source); err != nil {
			log.Printf("Error sending typing indicator: %v", err)
		}

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := t.typingFunc(source); err != nil {
					log.Printf("Error sending typing indicator: %v", err)
				}
			}
		}
	}()
}

// Stop stops the typing indicator for the given source channel.
func (t *TypingIndicator) Stop(source string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if stop, exists := t.stops[source]; exists {
		close(stop)
		delete(t.stops, source)
	}
}

// Refresh re-triggers the typing indicator for the given source channel.
// This should be called after sending messages since Discord clears the
// typing indicator when a message is posted.
func (t *TypingIndicator) Refresh(source string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Only refresh if we have an active typing indicator for this source
	if _, exists := t.stops[source]; exists {
		_ = t.typingFunc(source)
	}
}
