package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// TestPromptErrorResponseDeliveredToClient verifies that a JSON-RPC error
// response to an outbound session/prompt request is delivered to the Client as a
// ResponseError, rather than being silently dropped. Dropping it would leave any
// caller awaiting the PromptResponse blocked forever (e.g. arena hanging on a
// rate-limit error from the agent).
func TestPromptErrorResponseDeliveredToClient(t *testing.T) {
	agentReads, clientWrites := io.Pipe()
	clientReads, agentWrites := io.Pipe()

	got := make(chan any, 8)
	client := func(ctx context.Context, rid *json.RawMessage, msg any) {
		got <- msg
	}
	c := NewClientSideConnection(client, clientWrites, clientReads)

	// Scripted agent: answer session/prompt with an error response, mimicking an
	// agent that hit an upstream rate limit mid-turn.
	go func() {
		dec := json.NewDecoder(agentReads)
		for {
			var req anyMessage
			if err := dec.Decode(&req); err != nil {
				return
			}
			if req.Method == AgentMethodSessionPrompt {
				msg := anyMessage{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   NewInternalError(map[string]any{"message": "429 Too Many Requests"}),
				}
				b, _ := json.Marshal(msg)
				_, _ = agentWrites.Write(append(b, '\n'))
			}
		}
	}()

	ctx := context.Background()
	if err := c.Send(ctx, PromptRequest{SessionId: "s1", Prompt: []ContentBlock{TextBlock("hi")}}); err != nil {
		t.Fatalf("send prompt: %v", err)
	}

	select {
	case msg := <-got:
		re, ok := msg.(ResponseError)
		if !ok {
			t.Fatalf("expected ResponseError, got %T (%v)", msg, msg)
		}
		if re.Method != AgentMethodSessionPrompt {
			t.Fatalf("method = %q, want %q", re.Method, AgentMethodSessionPrompt)
		}
		if re.Err == nil {
			t.Fatal("ResponseError.Err is nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: error response was never delivered to the client")
	}
}
