package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/acp-go-sdk"
)

// ocSSEEvent represents a raw SSE event from the OpenCode server.
type ocSSEEvent struct {
	Type string
	Data string
}

// ocEventEnvelope is the JSON payload of an SSE event.
type ocEventEnvelope struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// ocPartUpdatedProps is the properties for "message.part.updated" events.
type ocPartUpdatedProps struct {
	Part  ocPart  `json:"part"`
	Delta *string `json:"delta,omitempty"`
}

// ocMessageUpdatedProps is the properties for "message.updated" events.
type ocMessageUpdatedProps struct {
	Info  ocMessage `json:"info"`
	Parts []ocPart  `json:"parts,omitempty"`
}

// ocSessionStatusProps is the properties for "session.status" events.
type ocSessionStatusProps struct {
	SessionID string         `json:"sessionID"`
	Status    ocSessionState `json:"status"`
}

// ocSessionState represents the session status.
type ocSessionState struct {
	Type string `json:"type"` // "idle", "busy", "retry"
}

// ocPart represents a message part from OpenCode.
type ocPart struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionID"`
	MessageID string          `json:"messageID"`
	Type      string          `json:"type"` // "text", "tool", "reasoning", "step-start", "step-finish", etc.
	Text      string          `json:"text,omitempty"`
	CallID    string          `json:"callID,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	State     json.RawMessage `json:"state,omitempty"`
}

// ocToolState represents the state of a tool part.
type ocToolState struct {
	Status   string         `json:"status"` // "pending", "running", "completed", "error"
	Input    map[string]any `json:"input,omitempty"`
	Output   string         `json:"output,omitempty"`
	Title    string         `json:"title,omitempty"`
	Error    string         `json:"error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ocMessage represents a message from OpenCode.
type ocMessage struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionID"`
	Role       string `json:"role"` // "user", "assistant"
	ProviderID string `json:"providerID,omitempty"`
	ModelID    string `json:"modelID,omitempty"`
	Cost       float64 `json:"cost,omitempty"`
	Tokens     *ocTokens `json:"tokens,omitempty"`
	Error      json.RawMessage `json:"error,omitempty"`
	Finish     string  `json:"finish,omitempty"`
}

// ocTokens represents token usage from an OpenCode assistant message.
type ocTokens struct {
	Input     int64    `json:"input"`
	Output    int64    `json:"output"`
	Reasoning int64    `json:"reasoning"`
	Cache     ocCache  `json:"cache"`
}

// ocCache represents cache token usage.
type ocCache struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

// openCodeSSE manages the SSE connection to an OpenCode server and translates
// events into acp.SessionUpdate values sent via the emit callback.
type openCodeSSE struct {
	baseURL   string
	sessionID string
	emit      func(update acp.SessionUpdate)
	client    *http.Client
	logger    *slog.Logger

	// onMessage is called when a message.updated event arrives (for usage tracking)
	onMessage func(msg ocMessage)
	// onStatusChange is called when session status changes
	onStatusChange func(status ocSessionState)
}

// run connects to the SSE stream and processes events until ctx is cancelled.
func (s *openCodeSSE) run(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", s.baseURL+"/event", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var currentEvent ocSSEEvent

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Empty line means end of event
			if currentEvent.Data != "" {
				s.processEvent(currentEvent)
			}
			currentEvent = ocSSEEvent{}
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent.Type = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			currentEvent.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		// Ignore other fields (id:, retry:, comments)
	}

	return scanner.Err()
}

// processEvent handles a single SSE event.
func (s *openCodeSSE) processEvent(event ocSSEEvent) {
	if event.Data == "" {
		return
	}

	var envelope ocEventEnvelope
	if err := json.Unmarshal([]byte(event.Data), &envelope); err != nil {
		s.logger.Warn("failed to parse SSE event", "error", err, "data", event.Data)
		return
	}

	switch envelope.Type {
	case "message.part.updated":
		s.handlePartUpdated(envelope.Properties)
	case "message.updated":
		s.handleMessageUpdated(envelope.Properties)
	case "session.status":
		s.handleSessionStatus(envelope.Properties)
	}
}

// handlePartUpdated translates message.part.updated events.
func (s *openCodeSSE) handlePartUpdated(raw json.RawMessage) {
	var props ocPartUpdatedProps
	if err := json.Unmarshal(raw, &props); err != nil {
		s.logger.Warn("failed to parse part updated", "error", err)
		return
	}

	// Filter to our session only
	if props.Part.SessionID != s.sessionID {
		return
	}

	switch props.Part.Type {
	case "text":
		// Streaming text chunk
		text := ""
		if props.Delta != nil {
			text = *props.Delta
		}
		if text == "" {
			return
		}
		s.emit(acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Content: acp.ContentBlock{
					Text: &acp.ContentBlockText{
						Text: text,
						Type: "text",
					},
				},
			},
		})

	case "tool":
		s.handleToolPart(props.Part)
	}
}

// handleToolPart translates tool part events into ACP tool call updates.
func (s *openCodeSSE) handleToolPart(part ocPart) {
	if len(part.State) == 0 || part.CallID == "" {
		return
	}

	var state ocToolState
	if err := json.Unmarshal(part.State, &state); err != nil {
		s.logger.Warn("failed to parse tool state", "error", err)
		return
	}

	toolCallID := acp.ToolCallId(part.CallID)
	title := state.Title
	if title == "" {
		title = part.Tool
	}

	switch state.Status {
	case "pending":
		kind := acp.ToolKind(part.Tool)
		status := acp.ToolCallStatusPending
		s.emit(acp.SessionUpdate{
			ToolCall: &acp.SessionUpdateToolCall{
				ToolCallId: toolCallID,
				Title:      title,
				Kind:       kind,
				Status:     status,
				RawInput:   state.Input,
			},
		})

	case "running":
		status := acp.ToolCallStatusInProgress
		s.emit(acp.SessionUpdate{
			ToolCallUpdate: &acp.SessionToolCallUpdate{
				ToolCallId: toolCallID,
				Title:      &title,
				Status:     &status,
				RawInput:   state.Input,
			},
		})

	case "completed":
		status := acp.ToolCallStatusCompleted
		s.emit(acp.SessionUpdate{
			ToolCallUpdate: &acp.SessionToolCallUpdate{
				ToolCallId: toolCallID,
				Title:      &title,
				Status:     &status,
				RawInput:   state.Input,
			},
		})

	case "error":
		status := acp.ToolCallStatusFailed
		errorTitle := title
		if state.Error != "" {
			errorTitle = title + ": " + state.Error
		}
		s.emit(acp.SessionUpdate{
			ToolCallUpdate: &acp.SessionToolCallUpdate{
				ToolCallId: toolCallID,
				Title:      &errorTitle,
				Status:     &status,
				RawInput:   state.Input,
			},
		})
	}
}

// handleMessageUpdated processes message.updated events for usage tracking.
func (s *openCodeSSE) handleMessageUpdated(raw json.RawMessage) {
	var props ocMessageUpdatedProps
	if err := json.Unmarshal(raw, &props); err != nil {
		return
	}
	if props.Info.SessionID != s.sessionID {
		return
	}
	if s.onMessage != nil {
		s.onMessage(props.Info)
	}
}

// handleSessionStatus processes session.status events.
func (s *openCodeSSE) handleSessionStatus(raw json.RawMessage) {
	var props ocSessionStatusProps
	if err := json.Unmarshal(raw, &props); err != nil {
		return
	}
	if props.SessionID != s.sessionID {
		return
	}
	if s.onStatusChange != nil {
		s.onStatusChange(props.Status)
	}
}
