package tck

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/types"
)

// Turn is everything observed for a single probe prompt.
type Turn struct {
	Tag         string
	Text        string
	ToolCalls   []acp.SessionUpdateToolCall
	Usage       []acp.SessionUsageUpdate
	StopReason  acp.StopReason
	GotResponse bool
}

// Transcript records every protocol fact observed while running a scenario
// against one agent. Record is registered as a router.Subscriber, so it is
// mutated from the connection's receive goroutine while the Runner sets the
// active probe tag and (after the scenario) checks read the data — all guarded
// by mu.
type Transcript struct {
	mu sync.Mutex

	// ProbeFile is the unique filename pre-created in the working directory; the
	// list-dir check looks for it in the agent's answer.
	ProbeFile string

	Init     acp.InitializeResponse
	Session  acp.NewSessionResponse
	Commands []acp.AvailableCommand
	Turns    map[string]*Turn
	Order    []string
	MetaKeys map[string]bool

	active string
}

// NewTranscript returns an empty transcript ready to record.
func NewTranscript() *Transcript {
	return &Transcript{
		Turns:    map[string]*Turn{},
		MetaKeys: map[string]bool{},
	}
}

// SetInit stores the agent's initialize response. It is fetched from the router
// after WaitReady because the initialize response is not fanned out to
// subscribers.
func (t *Transcript) SetInit(init acp.InitializeResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Init = init
	t.harvestMeta(init.Meta)
	if init.AgentInfo != nil {
		t.harvestMeta(init.AgentInfo.Meta)
	}
}

// Begin marks tag as the active probe; subsequent turn-scoped updates (text,
// tool calls, usage, stop reason) are attributed to it.
func (t *Transcript) Begin(tag string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = tag
	if _, ok := t.Turns[tag]; !ok {
		t.Turns[tag] = &Turn{Tag: tag}
		t.Order = append(t.Order, tag)
	}
}

// Record is the router.Subscriber that accumulates observations. Unknown message
// types are ignored.
func (t *Transcript) Record(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch m := msg.(type) {
	case acp.NewSessionResponse:
		t.Session = m
		t.harvestMeta(m.Meta)
	case acp.SessionNotification:
		t.harvestMeta(m.Meta)
		t.recordUpdate(m.Update)
	case acp.PromptResponse:
		if cur := t.Turns[t.active]; cur != nil {
			cur.StopReason = m.StopReason
			cur.GotResponse = true
		}
		t.harvestMeta(m.Meta)
	}
}

// recordUpdate dispatches a single session/update notification. Caller holds mu.
func (t *Transcript) recordUpdate(u acp.SessionUpdate) {
	cur := t.Turns[t.active]
	switch {
	case u.AgentMessageChunk != nil:
		t.harvestMeta(u.AgentMessageChunk.Meta)
		if cur != nil && u.AgentMessageChunk.Content.Text != nil {
			cur.Text += u.AgentMessageChunk.Content.Text.Text
		}
	case u.ToolCall != nil:
		t.harvestMeta(u.ToolCall.Meta)
		if cur != nil {
			cur.ToolCalls = append(cur.ToolCalls, *u.ToolCall)
		}
	case u.UsageUpdate != nil:
		t.harvestMeta(u.UsageUpdate.Meta)
		if cur != nil {
			cur.Usage = append(cur.Usage, *u.UsageUpdate)
		}
	case u.AvailableCommandsUpdate != nil:
		t.harvestMeta(u.AvailableCommandsUpdate.Meta)
		// The update carries the full list; keep the latest non-empty one.
		if len(u.AvailableCommandsUpdate.AvailableCommands) > 0 {
			t.Commands = u.AvailableCommandsUpdate.AvailableCommands
		}
	}
}

// harvestMeta records every distinct _meta key seen anywhere in the stream.
// Caller holds mu.
func (t *Transcript) harvestMeta(meta map[string]any) {
	for k := range meta {
		t.MetaKeys[k] = true
	}
}
