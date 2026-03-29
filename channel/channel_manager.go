package channel

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// ChannelManager is a registry that creates, stores, and closes Channel instances.
type ChannelManager struct {
	mu        sync.RWMutex
	factories map[ChannelID]CreateChannel
	channels  map[ChannelID]Channel
}

// NewChannelManager creates a new ChannelManager.
func NewChannelManager() *ChannelManager {
	return &ChannelManager{
		factories: make(map[ChannelID]CreateChannel),
		channels:  make(map[ChannelID]Channel),
	}
}

// Register registers a channel factory under the given ID.
// The channel will be created when Start is called.
func (cm *ChannelManager) Register(id ChannelID, factory CreateChannel) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.factories[id] = factory
}

// Start creates all registered channels using their factories.
func (cm *ChannelManager) Start(ctx context.Context) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, factory := range cm.factories {
		ch, err := factory(ctx)
		if err != nil {
			// Close any channels already started
			for _, started := range cm.channels {
				if closeErr := started.Close(); closeErr != nil {
					slog.Error("failed to close channel during rollback", "err", closeErr)
				}
			}
			cm.channels = make(map[ChannelID]Channel)
			return fmt.Errorf("failed to start channel %s: %w", id, err)
		}
		cm.channels[id] = ch
	}
	return nil
}

// Close shuts down all channels.
func (cm *ChannelManager) Close() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, ch := range cm.channels {
		if err := ch.Close(); err != nil {
			slog.Error("failed to close channel", "channel", id, "err", err)
		}
	}
	cm.channels = make(map[ChannelID]Channel)
}

// Get returns a channel by ID.
func (cm *ChannelManager) Get(id ChannelID) (Channel, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	ch, ok := cm.channels[id]
	return ch, ok
}

// All returns a snapshot of all active channels.
func (cm *ChannelManager) All() map[ChannelID]Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make(map[ChannelID]Channel, len(cm.channels))
	for k, v := range cm.channels {
		result[k] = v
	}
	return result
}
