package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/sandbox"
	"github.com/elek/acpp/types"
)

// WebChannel bridges the web UI to the router. It is a router.Subscriber: every
// raw ACP update flowing through the router is translated into the JSON event
// shape the browser already understands (agent_message_chunk, tool_call, plan,
// …) and published to the Hub so any connected WebSocket client receives it live.
// It also creates and closes conversations on behalf of HTTP handlers (it is the
// Server's SessionCreator and SessionCloser).
//
// Persistence is handled separately by the persistence package, which subscribes
// to the same router; WebChannel only deals with the live browser transport.
//
// Conversations are keyed externally by their ACP session id string — what the
// frontend uses in URLs — while internally each maps to a
// types.ConversationMeta used for prompt routing and closing.
type WebChannel struct {
	router *router.Router
	hub    *Hub

	mu   sync.Mutex
	byID map[string]types.ConversationMeta // session id string -> conversation meta
}

var _ router.Subscriber = (*WebChannel)(nil).Receive

// NewWebChannel creates a WebChannel, subscribes it to the router so it receives
// every conversation's updates, and returns it. Call before creating
// conversations so no early updates are missed.
func NewWebChannel(rt *router.Router, hub *Hub) *WebChannel {
	c := &WebChannel{
		router: rt,
		hub:    hub,
		byID:   make(map[string]types.ConversationMeta),
	}
	rt.Subscribe(c.Receive)
	return c
}

// Receive translates one raw router event into a browser event and publishes it
// to the Hub keyed by the conversation's ACP session id.
func (c *WebChannel) Receive(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	switch m := msg.(type) {
	case acp.SessionNotification:
		raw, eventType := db.MarshalEvent(m.Update)
		c.publish(string(id.SessionID), eventType, raw)
	case acp.PromptRequest:
		// Echo the user's prompt so the browser renders it, mirroring how
		// persisted history replays.
		var text string
		if len(m.Prompt) > 0 && m.Prompt[0].Text != nil {
			text = m.Prompt[0].Text.Text
		}
		payload, _ := json.Marshal(map[string]string{"prompt": text})
		c.publish(string(id.SessionID), "prompt", payload)
	case acp.PromptResponse:
		// The turn has finished; the frontend draws a separator on this event.
		c.publish(string(id.SessionID), "prompt_finished", json.RawMessage(`{}`))
	case types.ConversationReplaced:
		c.handleReplaced(m)
	}
}

// handleReplaced re-keys a conversation after /clear restarts it and tells the
// old session's page to navigate to the new one. The new session's row is written
// by the persistence subscriber off the same session/new response.
func (c *WebChannel) handleReplaced(rep types.ConversationReplaced) {
	oldID := string(rep.Old.SessionID)
	newID := string(rep.New.SessionID)

	c.mu.Lock()
	_, ok := c.byID[oldID]
	if ok {
		delete(c.byID, oldID)
		c.byID[newID] = rep.New
	}
	c.mu.Unlock()
	if !ok {
		return
	}

	payload, _ := json.Marshal(map[string]string{"new_session_id": newID})
	// Publish on the OLD id: that's the page currently holding the WebSocket.
	c.hub.Publish(oldID, logEntry{EventType: "session_replaced", Payload: payload})
}

// publish sends one event to live WebSocket subscribers of a session.
func (c *WebChannel) publish(sessionID, eventType string, raw json.RawMessage) {
	c.hub.Publish(sessionID, logEntry{EventType: eventType, Payload: raw})
}

// StartSessionWeb creates a new conversation through the router and returns its
// ACP session id, which the frontend uses as the session URL key. Implements
// SessionCreator.
func (c *WebChannel) StartSessionWeb(dir, agent, sandboxType, sandboxProfiles, projectName string) (string, error) {
	opts := types.SessionOpts{
		ProjectID:   projectName,
		Agent:       agent,
		CWD:         dir,
		Source:      "web",
		SandboxType: sandboxType,
	}
	if sandboxType != "" {
		sb, err := sandbox.ResolveSandbox(sandboxType, sandboxProfiles, dir)
		if err != nil {
			return "", err
		}
		opts.Sandbox = sb
	}

	id, err := c.router.Create(context.Background(), opts)
	if err != nil {
		return "", err
	}
	// The web UI keys sessions by their ACP session id (used in URLs and DB rows),
	// which the async handshake fills in after Create returns — wait for it. By the
	// time WaitReady returns, the persistence subscriber has written the session
	// row (the session/new response is fanned out before WaitReady unblocks).
	id, err = c.router.WaitReady(context.Background(), id)
	if err != nil {
		return "", err
	}

	sessionID := string(id.SessionID)
	c.mu.Lock()
	c.byID[sessionID] = id
	c.mu.Unlock()
	return sessionID, nil
}

// SubmitPrompt routes a user prompt to the conversation backing sessionID. A
// harness command (e.g. /clear, /help, !ls) is echoed to the browser as a
// transient "command" event, executed by the router, and its feedback published
// as a "command_response" event — both browser-only, never persisted. A normal
// prompt is echoed as a "prompt" event (via the router fanning the raw
// PromptRequest out to subscribers) so it renders immediately.
func (c *WebChannel) SubmitPrompt(sessionID, prompt string) error {
	c.mu.Lock()
	conv, ok := c.byID[sessionID]
	c.mu.Unlock()
	if !ok {
		return errUnknownSession
	}

	// Echo a recognised command before running it: /clear navigates away and
	// /exit shuts the app down, so the echo has to be published first to be seen.
	if router.IsCommand(prompt) {
		payload, _ := json.Marshal(map[string]string{"text": strings.TrimSpace(prompt)})
		c.publish(sessionID, "command", payload)
	}

	go func() {
		ctx := context.Background()
		// A harness command (e.g. /clear) is not a prompt. Its feedback is surfaced
		// transiently to the browser rather than persisted like agent output.
		if handled, feedback, err := c.router.HandleCommands(ctx, conv, prompt); err != nil {
			c.publishCommandResponse(sessionID, "⚠️ "+err.Error())
			return
		} else if handled {
			if feedback != "" {
				c.publishCommandResponse(sessionID, feedback)
			}
			return
		}
		meta, err := c.router.WaitReady(ctx, conv)
		if err != nil {
			c.reportSubmitErr(sessionID, err)
			return
		}
		if err := c.router.Send(ctx, meta, acp.PromptRequest{
			SessionId: meta.SessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
		}); err != nil {
			c.reportSubmitErr(sessionID, err)
		}
	}()
	return nil
}

// publishCommandResponse surfaces a command's textual feedback to the browser as
// a transient "command_response" event. It is published directly to the Hub —
// never through the router — so command output is not persisted or replayed.
func (c *WebChannel) publishCommandResponse(sessionID, text string) {
	payload, _ := json.Marshal(map[string]string{"text": text})
	c.publish(sessionID, "command_response", payload)
}

// reportSubmitErr logs a failed prompt submission and surfaces it to the browser
// as a system text message.
func (c *WebChannel) reportSubmitErr(sessionID string, err error) {
	slog.Error("web: submit prompt", "session", sessionID, "error", err)
	ep, _ := json.Marshal(map[string]string{"text": "⚠️ " + err.Error()})
	c.publish(sessionID, "text_message", ep)
}

// CloseSession shuts down the conversation backing sessionID. Implements
// SessionCloser.
func (c *WebChannel) CloseSession(sessionID string) {
	c.mu.Lock()
	conv, ok := c.byID[sessionID]
	if ok {
		delete(c.byID, sessionID)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	c.router.CloseConversation(conv)
}

var errUnknownSession = unknownSessionError{}

type unknownSessionError struct{}

func (unknownSessionError) Error() string { return "unknown session" }
