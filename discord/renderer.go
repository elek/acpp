package discord

import (
	"encoding/base64"
	"strings"

	"github.com/elek/acpp/acp"
)

// RenderSink is the set of primitive rendering operations an integration
// provides. A Renderer translates raw ACP session updates into calls on the
// sink. This keeps the per-event translation logic in one place while letting
// each integration own how a fragment / tool call / plan is actually displayed.
//
// The source string identifies the rendering target within the integration (a
// Discord channel ID, "console", etc.); the renderer treats it as opaque and
// passes it back on every call.
type RenderSink interface {
	SendTextFragment(source string, text string) error
	SendThoughtFragment(source string, text string) error
	SendToolUsage(source string, toolCallID string) ToolUsageUpdater
	SendImage(source string, mimeType string, data []byte) error
	SendPlanUpdate(source string, plan PlanUpdate) error
}

// Renderer translates raw acp.SessionUpdate events into RenderSink calls for a
// single rendering target (source). It owns the per-tool-call state (cache and
// the live updater handle) for that target.
//
// Integrations create one Renderer per source and feed it the raw updates they
// receive; the rendering responsibility thus lives inside the integration
// rather than in a central translation layer.
type Renderer struct {
	sink   RenderSink
	source string

	toolUsageCache map[acp.ToolCallId]ToolUsage
	toolUpdaters   map[acp.ToolCallId]ToolUsageUpdater
}

// NewRenderer creates a Renderer that drives sink for the given source.
func NewRenderer(sink RenderSink, source string) *Renderer {
	return &Renderer{
		sink:           sink,
		source:         source,
		toolUsageCache: make(map[acp.ToolCallId]ToolUsage),
		toolUpdaters:   make(map[acp.ToolCallId]ToolUsageUpdater),
	}
}

// Handle renders a single ACP session update.
func (r *Renderer) Handle(event acp.SessionUpdate) {
	switch {
	case event.AgentMessageChunk != nil:
		content := event.AgentMessageChunk.Content
		if content.Text != nil {
			_ = r.sink.SendTextFragment(r.source, content.Text.Text)
		} else if content.Image != nil {
			if data, err := base64.StdEncoding.DecodeString(content.Image.Data); err == nil {
				_ = r.sink.SendImage(r.source, content.Image.MimeType, data)
			}
		}

	case event.AgentThoughtChunk != nil:
		content := event.AgentThoughtChunk.Content
		if content.Text != nil {
			_ = r.sink.SendThoughtFragment(r.source, content.Text.Text)
		}

	case event.ToolCall != nil:
		tc := event.ToolCall
		usage := buildToolUsage(&tc.Title, &tc.Kind, &tc.Status, tc.RawInput)
		r.toolUsageCache[tc.ToolCallId] = usage
		r.applyToolUpdate(tc.ToolCallId, usage)
		if tc.Status == acp.ToolCallStatusCompleted || tc.Status == acp.ToolCallStatusFailed {
			r.cleanupTool(tc.ToolCallId)
		}

	case event.Plan != nil:
		plan := event.Plan
		update := PlanUpdate{Entries: make([]PlanEntry, len(plan.Entries))}
		for i, entry := range plan.Entries {
			update.Entries[i] = PlanEntry{
				Content:  entry.Content,
				Priority: string(entry.Priority),
				Status:   string(entry.Status),
			}
		}
		_ = r.sink.SendPlanUpdate(r.source, update)

	case event.ToolCallUpdate != nil:
		update := event.ToolCallUpdate
		cached, exists := r.toolUsageCache[update.ToolCallId]
		if !exists {
			cached = ToolUsage{Input: make(map[string]string)}
		}
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
		r.applyToolUpdate(update.ToolCallId, cached)
		if update.Status != nil && (*update.Status == acp.ToolCallStatusCompleted || *update.Status == acp.ToolCallStatusFailed) {
			r.cleanupTool(update.ToolCallId)
		}
	}
}

// applyToolUpdate sends a tool usage update via the sink, creating the
// per-tool-call updater lazily on first use.
func (r *Renderer) applyToolUpdate(toolCallId acp.ToolCallId, usage ToolUsage) {
	updater, ok := r.toolUpdaters[toolCallId]
	if !ok {
		updater = r.sink.SendToolUsage(r.source, string(toolCallId))
		r.toolUpdaters[toolCallId] = updater
	}
	_ = updater(usage)
}

func (r *Renderer) cleanupTool(toolCallId acp.ToolCallId) {
	delete(r.toolUsageCache, toolCallId)
	delete(r.toolUpdaters, toolCallId)
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

// ToolUsage is the rendered state of a single tool call. Integrations translate
// raw ACP tool-call updates into this shape before displaying them.
type ToolUsage struct {
	Name     string
	Title    string            // Original title, used to decide whether to show input
	Input    map[string]string // All input parameters
	Status   string
	ToolKind string // Tool kind for emoji selection
}

// ToolUsageUpdater applies an updated ToolUsage to a live display handle (e.g. an
// editable message). It is returned once per tool call and invoked on every update.
type ToolUsageUpdater func(ToolUsage) error

// PlanUpdate represents a plan update to be displayed.
type PlanUpdate struct {
	Entries []PlanEntry
}

// PlanEntry represents a single entry in a plan.
type PlanEntry struct {
	Content  string
	Priority string // "high", "medium", "low"
	Status   string // "pending", "in_progress", "completed"
}
