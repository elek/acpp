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

	// Secret is the unique token planted in the first session by the "memo" probe.
	// The resume check looks for it in the answer the resumed session gives, which
	// is only possible if the prior conversation's context was restored.
	Secret string

	// ResumeSkipped, when non-empty, explains why the resume phase did not run
	// (e.g. the agent does not advertise the loadSession capability).
	ResumeSkipped string
	// ResumeErr, when non-empty, is the failure that stopped the resume phase
	// (session/load rejected, handshake timeout, …).
	ResumeErr string

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
// tool calls, usage, stop reason) are attributed to it, and the turn joins the
// probe order that the turn-spanning checks iterate.
func (t *Transcript) Begin(tag string) {
	t.begin(tag, true)
}

// BeginSink is Begin for a bucket that is not a probe: it collects updates the
// agent emits outside a prompt turn (the history a resuming agent replays while
// answering session/load) so they are not misattributed to the preceding probe.
// The turn stays out of Order, so checks that aggregate over probes — prompt
// completion, tool usage — ignore it.
func (t *Transcript) BeginSink(tag string) {
	t.begin(tag, false)
}

func (t *Transcript) begin(tag string, probe bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = tag
	if _, ok := t.Turns[tag]; !ok {
		t.Turns[tag] = &Turn{Tag: tag}
		if probe {
			t.Order = append(t.Order, tag)
		}
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
