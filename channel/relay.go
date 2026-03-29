package channel

import (
	"encoding/base64"
	"strings"
	"sync"

	"github.com/coder/acp-go-sdk"
	acp2 "github.com/elek/acpp/acp"
)

// BroadcastFunc is called by the Relay to fan out channel operations
// to all subscribed endpoints for a session.
type BroadcastFunc func(func(Channel, SourceID))

// Relay translates ACP session update events into Channel operations.
// It uses a broadcast function provided by the Gateway to fan out
// updates to all subscribed channel endpoints.
type Relay struct {
	sessionID string
	sess      acp2.Session
	broadcast BroadcastFunc

	// ready is closed when the first SessionEvent (readiness signal) arrives
	ready chan struct{}

	mu sync.Mutex // protects toolUsageCache and toolUpdaters
	// Cache for tool usages, keyed by tool call ID
	toolUsageCache map[acp.ToolCallId]ToolUsage
	// Tool updaters are per-ChannelSource because each channel endpoint
	// (e.g., each Discord message) has its own edit handle.
	toolUpdaters map[ChannelSource]map[acp.ToolCallId]ToolUsageUpdater

	// barrierNotify is used to wake the event loop when a new barrier is added.
	// WaitForPending sends on this channel; the event loop selects on it.
	barrierNotify chan struct{}
	// barriers is a queue of channels used by WaitForPending to synchronize
	// with the event loop. Each barrier is closed by the event loop after
	// all events queued before the barrier have been processed.
	barrierMu sync.Mutex
	barriers  []chan struct{}
}

// Session returns the underlying session.
func (r *Relay) Session() acp2.Session {
	return r.sess
}

// Ready returns a channel that is closed when the underlying session is ready.
func (r *Relay) Ready() <-chan struct{} {
	return r.ready
}

// NewRelay creates a Relay that owns a Session and consumes its updates.
// The broadcast function is called for each update to fan out to subscribed endpoints.
// The onUpdate hook is called for each event (e.g., for DB logging).
func NewRelay(sess acp2.Session, broadcast BroadcastFunc, onUpdate func(acp2.SessionEvent)) *Relay {
	r := &Relay{
		sessionID:      sess.GetID(),
		sess:           sess,
		broadcast:      broadcast,
		ready:          make(chan struct{}),
		toolUsageCache: make(map[acp.ToolCallId]ToolUsage),
		toolUpdaters:   make(map[ChannelSource]map[acp.ToolCallId]ToolUsageUpdater),
		barrierNotify:  make(chan struct{}, 1),
	}

	// Consume updates from the session channel in the background.
	go func() {
		updates := sess.Ready()
		first := true
		for {
			// Wait for either a new event or a barrier notification.
			select {
			case event, ok := <-updates:
				if !ok {
					// Channel closed.
					if first {
						close(r.ready)
					}
					r.flushBarriers()
					return
				}
				if first {
					close(r.ready)
					first = false
					// The first event is a readiness signal with a zero-value
					// SessionUpdate (all nil fields). Skip onUpdate and HandleUpdate
					// because MarshalJSON returns empty bytes for zero-value
					// SessionUpdate, which causes "unexpected end of JSON input"
					// when stored/read back.
					continue
				}
				if onUpdate != nil {
					onUpdate(event)
				}
				r.HandleUpdate(event.Update)

				// Drain all immediately available events so that
				// barriers only fire once the buffer is empty.
			drain:
				for {
					select {
					case event, ok := <-updates:
						if !ok {
							r.flushBarriers()
							return
						}
						if onUpdate != nil {
							onUpdate(event)
						}
						r.HandleUpdate(event.Update)
					default:
						break drain
					}
				}
				r.flushBarriers()

			case <-r.barrierNotify:
				// Woken by WaitForPending. No events were pending,
				// so flush barriers immediately.
				r.flushBarriers()
			}
		}
	}()

	return r
}

// NewRelayForReplay creates a Relay for replay/testing that sends to a single channel endpoint.
func NewRelayForReplay(s SourceID, ch Channel) *Relay {
	return &Relay{
		broadcast: func(fn func(Channel, SourceID)) {
			fn(ch, s)
		},
		toolUsageCache: make(map[acp.ToolCallId]ToolUsage),
		toolUpdaters:   make(map[ChannelSource]map[acp.ToolCallId]ToolUsageUpdater),
	}
}

// WaitForPending blocks until the Relay has processed all events that were
// in the updates channel at the time of the call. This is used after
// Prompt() returns to ensure all streaming fragments have been broadcast
// to channels before flushing buffers.
func (r *Relay) WaitForPending() {
	ch := make(chan struct{})
	r.barrierMu.Lock()
	r.barriers = append(r.barriers, ch)
	r.barrierMu.Unlock()
	// Wake the event loop in case it's blocked waiting for events.
	select {
	case r.barrierNotify <- struct{}{}:
	default:
	}
	<-ch
}

// flushBarriers closes all pending barrier channels, signalling that
// all events up to this point have been processed.
func (r *Relay) flushBarriers() {
	r.barrierMu.Lock()
	barriers := r.barriers
	r.barriers = nil
	r.barrierMu.Unlock()
	for _, b := range barriers {
		close(b)
	}
}

// HandleUpdate processes a single ACP session update event.
func (r *Relay) HandleUpdate(event acp.SessionUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch {
	case event.AgentMessageChunk != nil:
		content := event.AgentMessageChunk.Content
		if content.Text != nil {
			r.broadcast(func(ch Channel, s SourceID) {
				_ = ch.SendTextFragment(s, content.Text.Text)
			})
		} else if content.Image != nil {
			data, err := base64.StdEncoding.DecodeString(content.Image.Data)
			if err == nil {
				r.broadcast(func(ch Channel, s SourceID) {
					_ = ch.SendImage(s, content.Image.MimeType, data)
				})
			}
		}

	case event.AgentThoughtChunk != nil:
		content := event.AgentThoughtChunk.Content
		if content.Text != nil {
			r.broadcast(func(ch Channel, s SourceID) {
				_ = ch.SendThoughtFragment(s, content.Text.Text)
			})
		}

	case event.ToolCall != nil:
		tc := event.ToolCall

		toolUsage := buildToolUsage(&tc.Title, &tc.Kind, &tc.Status, tc.RawInput)
		r.toolUsageCache[tc.ToolCallId] = toolUsage

		r.broadcastToolUpdate(tc.ToolCallId, toolUsage)

		if tc.Status == acp.ToolCallStatusCompleted || tc.Status == acp.ToolCallStatusFailed {
			r.cleanupTool(tc.ToolCallId)
		}

	case event.Plan != nil:
		plan := event.Plan
		update := PlanUpdate{
			Entries: make([]PlanEntry, len(plan.Entries)),
		}
		for i, entry := range plan.Entries {
			update.Entries[i] = PlanEntry{
				Content:  entry.Content,
				Priority: string(entry.Priority),
				Status:   string(entry.Status),
			}
		}
		r.broadcast(func(ch Channel, s SourceID) {
			_ = ch.SendPlanUpdate(s, update)
		})

	case event.ToolCallUpdate != nil:
		update := event.ToolCallUpdate

		cached, exists := r.toolUsageCache[update.ToolCallId]
		if !exists {
			cached = ToolUsage{Input: make(map[string]string)}
		}

		// Merge: only update fields if new value is provided
		if update.Title != nil && *update.Title != "" {
			cached.Name = buildName(update.Kind, update.Title)
			cached.Title = *update.Title
		} else if update.Kind != nil {
			cached.Name = buildName(update.Kind, nil)
		}
		if update.Kind != nil {
			cached.ToolKind = string(*update.Kind)
		}
		if update.RawInput != nil {
			input := extractInput(update.RawInput)
			if len(input) > 0 {
				if cached.Input == nil {
					cached.Input = make(map[string]string)
				}
				for k, v := range input {
					cached.Input[k] = v
				}
			}
		}
		if update.Status != nil {
			cached.Status = statusToString(*update.Status)
		}

		r.toolUsageCache[update.ToolCallId] = cached

		r.broadcastToolUpdate(update.ToolCallId, cached)

		if update.Status != nil && (*update.Status == acp.ToolCallStatusCompleted || *update.Status == acp.ToolCallStatusFailed) {
			r.cleanupTool(update.ToolCallId)
		}
	}
}

// broadcastToolUpdate sends a tool usage update to all subscribed endpoints.
// Creates per-endpoint updaters lazily. Must be called with mu held.
func (r *Relay) broadcastToolUpdate(toolCallId acp.ToolCallId, usage ToolUsage) {
	r.broadcast(func(ch Channel, s SourceID) {
		cs := ChannelSource{SourceID: s}
		updaters, ok := r.toolUpdaters[cs]
		if !ok {
			updaters = make(map[acp.ToolCallId]ToolUsageUpdater)
			r.toolUpdaters[cs] = updaters
		}
		updater, ok := updaters[toolCallId]
		if !ok {
			updater = ch.SendToolUsage(s, string(toolCallId))
			updaters[toolCallId] = updater
		}
		_ = updater(usage)
	})
}

// cleanupTool removes cached tool state. Must be called with mu held.
func (r *Relay) cleanupTool(toolCallId acp.ToolCallId) {
	delete(r.toolUsageCache, toolCallId)
	for cs, updaters := range r.toolUpdaters {
		delete(updaters, toolCallId)
		if len(updaters) == 0 {
			delete(r.toolUpdaters, cs)
		}
	}
}

func buildToolUsage(title *string, kind *acp.ToolKind, callStatus *acp.ToolCallStatus, rawInput any) ToolUsage {
	toolKind := ""
	if kind != nil {
		toolKind = string(*kind)
	}
	titleStr := ""
	if title != nil {
		titleStr = *title
	}
	return ToolUsage{
		Name:     buildName(kind, title),
		Title:    titleStr,
		Input:    extractInput(rawInput),
		Status:   statusToString(*callStatus),
		ToolKind: toolKind,
	}
}

func buildName(kind *acp.ToolKind, title *string) string {
	name := ""
	if kind != nil {
		name += string(*kind) + " "
	}
	if title != nil {
		name += *title + " "
	}
	return strings.TrimSpace(name)
}

func extractInput(rawInput any) map[string]string {
	result := make(map[string]string)
	if rawInput == nil {
		return result
	}
	if inputs, ok := rawInput.(map[string]interface{}); ok {
		for key, value := range inputs {
			if strVal, ok := value.(string); ok {
				result[key] = strVal
			}
		}
	}
	return result
}

func statusToString(callStatus acp.ToolCallStatus) string {
	switch callStatus {
	case acp.ToolCallStatusPending:
		return "pending"
	case acp.ToolCallStatusCompleted:
		return "completed"
	case acp.ToolCallStatusInProgress:
		return "in-progress"
	case acp.ToolCallStatusFailed:
		return "failed"
	}
	return ""
}
