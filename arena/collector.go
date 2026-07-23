package arena

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/types"
)

// terminal marks the end of a prompt turn.
type terminal struct {
	stopReason string
	err        string
}

// convBuf accumulates one conversation's output. done is closed once the turn
// reaches a terminal event (PromptResponse or ConversationClosed); term then
// holds that event and is safe to read.
type convBuf struct {
	mu   sync.Mutex
	text strings.Builder
	raw  [][]byte // each entry is a marshalled SessionUpdate (JSONL line)
	done chan struct{}
	term terminal
	once sync.Once
}

func (b *convBuf) finish(t terminal) {
	b.once.Do(func() {
		b.mu.Lock()
		b.term = t
		b.mu.Unlock()
		close(b.done)
	})
}

// terminalEvent returns the stored terminal (valid once done is closed).
func (b *convBuf) terminalEvent() terminal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.term
}

// collector is a router.Subscriber that buffers per-conversation output, keyed
// by the stable ConversationID, and signals turn completion on either a
// PromptResponse or a ConversationClosed (crash-safe, per scheduler prior art).
type collector struct {
	mu    sync.Mutex
	convs map[string]*convBuf
}

func newCollector() *collector {
	return &collector{convs: make(map[string]*convBuf)}
}

// register creates the buffer for a conversation before it is driven, so a
// waiter can never miss the terminal signal.
func (c *collector) register(convID string) *convBuf {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.convs[convID]
	if !ok {
		b = &convBuf{done: make(chan struct{})}
		c.convs[convID] = b
	}
	return b
}

func (c *collector) get(convID string) *convBuf {
	return c.register(convID)
}

func (c *collector) Receive(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	b := c.get(id.ConversationID)
	switch m := msg.(type) {
	case acp.SessionNotification:
		b.mu.Lock()
		if line, err := json.Marshal(m.Update); err == nil {
			b.raw = append(b.raw, line)
		}
		if ch := m.Update.AgentMessageChunk; ch != nil && ch.Content.Text != nil {
			b.text.WriteString(ch.Content.Text.Text)
		}
		b.mu.Unlock()
	case acp.PromptResponse:
		b.finish(terminal{stopReason: string(m.StopReason)})
	case types.ConversationClosed:
		b.finish(terminal{stopReason: "closed", err: m.Err})
	}
}
