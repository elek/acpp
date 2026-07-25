package arena

import (
	"context"
	"testing"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/types"
)

// TestCollectorResponseErrorFinishesTurn verifies that an errored response to a
// prompt (e.g. the agent hitting an upstream rate limit) ends the turn with its
// error, rather than leaving the waiter blocked until the parent context is
// cancelled.
func TestCollectorResponseErrorFinishesTurn(t *testing.T) {
	c := newCollector()
	id := types.ConversationMeta{ConversationID: "c1"}
	buf := c.register(id.ConversationID)

	c.Receive(context.Background(), nil, id, acp.ResponseError{
		Method: acp.AgentMethodSessionPrompt,
		Err:    acp.NewInternalError(map[string]any{"message": "429 Too Many Requests"}),
	})

	select {
	case <-buf.done:
	case <-time.After(time.Second):
		t.Fatal("collector did not finish the turn on ResponseError")
	}
	term := buf.terminalEvent()
	if term.err == "" {
		t.Fatal("terminal event carries no error message")
	}
}
