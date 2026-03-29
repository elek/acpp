package channel

import (
	"sync"

	"github.com/coder/acp-go-sdk"
)

// StubChannel is a fake Channel implementation for integration testing.
// It records all calls so tests can inspect what was sent.
type StubChannel struct {
	mu       sync.Mutex
	Messages []StubMessage
	inputCh  chan Input
}

// StubMessage records a single call to the channel.
type StubMessage struct {
	Kind   string // "fragment", "message", "tool", "plan", "start", "finish"
	Source SourceID
	Text   string
}

func (c *StubChannel) record(kind string, source SourceID, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Messages = append(c.Messages, StubMessage{Kind: kind, Source: source, Text: text})
}

// GetMessages returns a snapshot of all recorded messages.
func (c *StubChannel) GetMessages() []StubMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]StubMessage, len(c.Messages))
	copy(out, c.Messages)
	return out
}

func (c *StubChannel) SendTextFragment(source SourceID, text string) error {
	c.record("fragment", source, text)
	return nil
}

func (c *StubChannel) SendThoughtFragment(source SourceID, text string) error {
	c.record("thought", source, text)
	return nil
}

func (c *StubChannel) SendTextMessage(source SourceID, text string) error {
	c.record("message", source, text)
	return nil
}

func (c *StubChannel) SendImage(source SourceID, mimeType string, data []byte) error {
	c.record("image", source, mimeType)
	return nil
}

func (c *StubChannel) SendFile(source SourceID, filename string, content []byte) error {
	c.record("file", source, string(content))
	return nil
}

func (c *StubChannel) SendToolUsage(source SourceID, toolCallID string) ToolUsageUpdater {
	c.record("tool", source, toolCallID)
	return func(tu ToolUsage) error { return nil }
}

func (c *StubChannel) SendPlanUpdate(source SourceID, plan PlanUpdate) error {
	c.record("plan", source, "")
	return nil
}

func (c *StubChannel) StartConversation(source SourceID) error {
	c.record("start", source, "")
	return nil
}

func (c *StubChannel) FinishConversation(source SourceID, resp acp.PromptResponse) error {
	c.record("finish", source, "")
	return nil
}

func (c *StubChannel) GetTopic(source SourceID) (string, error) {
	return "", nil
}

func (c *StubChannel) GetChannelName(source SourceID) (string, error) {
	return string(source), nil
}

func (c *StubChannel) AskYesNo(source SourceID, question string) (bool, error) {
	return true, nil
}

func (c *StubChannel) Input() <-chan Input {
	if c.inputCh == nil {
		c.inputCh = make(chan Input)
	}
	return c.inputCh
}

func (c *StubChannel) Close() error {
	return nil
}
