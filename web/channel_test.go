package web

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/types"
)

// newTestChannel builds a WebChannel without a real router (the router is only
// needed for Create/Send/Close, which these tests don't exercise).
func newTestChannel(hub *Hub) *WebChannel {
	return &WebChannel{hub: hub, byID: make(map[string]types.ConversationMeta)}
}

func TestWebChannel_ReceivePublishesAgentMessage(t *testing.T) {
	hub := NewHub()
	c := newTestChannel(hub)

	sessionID := "sess-1"
	sub := hub.Subscribe(sessionID)
	defer hub.Unsubscribe(sessionID, sub)

	id := types.ConversationMeta{SessionID: acp.SessionId(sessionID)}
	update := acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock("hello")},
	}
	c.Receive(context.Background(), nil, id, acp.SessionNotification{Update: update})

	select {
	case raw := <-sub.ch:
		var entry logEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if entry.EventType != "agent_message_chunk" {
			t.Errorf("event_type = %q, want agent_message_chunk", entry.EventType)
		}
		// The payload must carry the flattened union shape the frontend reads:
		// a "content" object with the text the agent emitted.
		var payload struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload.SessionUpdate != "agent_message_chunk" {
			t.Errorf("payload.sessionUpdate = %q", payload.SessionUpdate)
		}
		if payload.Content.Text != "hello" {
			t.Errorf("payload.content.text = %q, want hello", payload.Content.Text)
		}
	default:
		t.Fatal("expected a message on the hub, got none")
	}
}

func TestWebChannel_PromptResponsePublishesFinished(t *testing.T) {
	hub := NewHub()
	c := newTestChannel(hub)

	sessionID := "sess-2"
	sub := hub.Subscribe(sessionID)
	defer hub.Unsubscribe(sessionID, sub)

	id := types.ConversationMeta{SessionID: acp.SessionId(sessionID)}
	c.Receive(context.Background(), nil, id, acp.PromptResponse{})

	select {
	case raw := <-sub.ch:
		var entry logEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if entry.EventType != "prompt_finished" {
			t.Errorf("event_type = %q, want prompt_finished", entry.EventType)
		}
	default:
		t.Fatal("expected prompt_finished on the hub, got none")
	}
}

func TestWebChannel_ConversationReplacedNavigatesOldSession(t *testing.T) {
	hub := NewHub()
	c := newTestChannel(hub)

	oldID := "old-sess"
	newID := "new-sess"
	old := types.ConversationMeta{SessionID: acp.SessionId(oldID)}
	c.byID[oldID] = old

	// The page holding the WebSocket is the OLD session's page.
	sub := hub.Subscribe(oldID)
	defer hub.Unsubscribe(oldID, sub)

	c.Receive(context.Background(), nil, old, types.ConversationReplaced{
		Old: old,
		New: types.ConversationMeta{SessionID: acp.SessionId(newID)},
	})

	select {
	case raw := <-sub.ch:
		var entry logEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if entry.EventType != "session_replaced" {
			t.Errorf("event_type = %q, want session_replaced", entry.EventType)
		}
		var payload map[string]string
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload["new_session_id"] != newID {
			t.Errorf("new_session_id = %q, want %q", payload["new_session_id"], newID)
		}
	default:
		t.Fatal("expected session_replaced on the hub, got none")
	}

	// The conversation must be re-keyed under the new id.
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byID[newID]; !ok {
		t.Error("conversation not re-keyed to new session id")
	}
	if _, ok := c.byID[oldID]; ok {
		t.Error("old session id still present after replace")
	}
}
