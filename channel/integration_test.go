package channel

import (
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/elek/acpp/acp"
)

func TestStubSessionIntegration(t *testing.T) {
	ch := &StubChannel{}
	source := SourceID("test-channel")

	opts := acp.SessionOpts{
		Source: "test",
		Agent:  "<STUB>",
		CWD:    "/tmp",
	}
	sess := acp.NewSession("test-session-1", opts)

	// Create relay for replay — sends to a single channel endpoint.
	relay := NewRelayForReplay(source, ch)

	// Start session (sends readiness event).
	if err := sess.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Consume updates in a goroutine, dispatching to the relay.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range sess.Ready() {
			relay.HandleUpdate(event.Update)
		}
	}()

	// Send a prompt and wait for the response.
	resp, err := sess.Prompt([]acpsdk.ContentBlock{acpsdk.TextBlock("say hello")})
	if err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}

	// Close the session so the updates channel drains.
	sess.Close()

	// Wait for the consumer goroutine to finish.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for update consumer to finish")
	}

	// Verify prompt response has a stop reason.
	if resp.StopReason != "end_turn" {
		t.Fatalf("unexpected stop reason: %q", resp.StopReason)
	}

	// Verify channel received the expected events.
	msgs := ch.GetMessages()

	// We expect: 1 tool usage + 3 text fragments = 4 messages minimum.
	if len(msgs) < 4 {
		t.Fatalf("expected at least 4 channel messages, got %d: %+v", len(msgs), msgs)
	}

	// First should be a tool call.
	if msgs[0].Kind != "tool" {
		t.Errorf("expected first message kind 'tool', got %q", msgs[0].Kind)
	}

	// Next three should be text fragments forming "Hello, world!".
	var assembled string
	for _, m := range msgs[1:] {
		if m.Kind == "fragment" {
			assembled += m.Text
		}
	}
	if assembled != "Hello, world!" {
		t.Errorf("expected assembled fragments 'Hello, world!', got %q", assembled)
	}
}
