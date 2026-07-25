package router

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// collector is a Subscriber that records the agent's streamed text so the test
// can assert on the full response.
type collector struct {
	t          *testing.T
	mu         sync.Mutex
	sb         strings.Builder
	stopReason acp.StopReason
	done       chan struct{} // closed when the turn's PromptResponse arrives
}

func (c *collector) Receive(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	switch m := msg.(type) {
	case acp.SessionNotification:
		switch u := m.Update; {
		case u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil:
			text := u.AgentMessageChunk.Content.Text.Text
			c.mu.Lock()
			c.sb.WriteString(text)
			c.mu.Unlock()
			c.t.Logf("agent_message_chunk: %s", text)
		case u.AgentThoughtChunk != nil && u.AgentThoughtChunk.Content.Text != nil:
			c.t.Logf("agent_thought_chunk: %s", u.AgentThoughtChunk.Content.Text.Text)
		case u.ToolCall != nil:
			c.t.Logf("tool_call: %s", u.ToolCall.Title)
		default:
			c.t.Logf("session/update for %s", m.SessionId)
		}
	case acp.PromptResponse:
		c.mu.Lock()
		c.stopReason = m.StopReason
		c.mu.Unlock()
		close(c.done)
	default:
		c.t.Logf("inbound: %T", msg)
	}
}

func (c *collector) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sb.String()
}

// TestClaude drives a full prompt round trip through the Router: it spawns the
// real claude-code-acp agent via the router's process manager, subscribes a
// listener, submits a prompt, and verifies the streamed response.
func TestClaude(t *testing.T) {
	const bin = "/home/elek/.npm-global/bin/claude-code-acp"
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("agent binary not available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	rt := New()
	defer rt.Close()

	col := &collector{t: t, done: make(chan struct{})}
	rt.Subscribe(col.Receive)

	id, err := rt.Create(ctx, types.SessionOpts{
		ProjectID: cwd,
		Agent:     bin,
		CWD:       cwd,
	})
	require.NoError(t, err)

	// Create returns before the async handshake; wait for the session id.
	id, err = rt.WaitReady(ctx, id)
	require.NoError(t, err)
	require.NotEmpty(t, id.SessionID)

	// Send returns immediately; the turn completes asynchronously, so wait for
	// the collector to observe the PromptResponse.
	err = rt.Send(ctx, id, acp.PromptRequest{
		SessionId: id.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("What is the capital of Spain?")},
	})
	require.NoError(t, err)

	select {
	case <-col.done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for prompt response: %v", ctx.Err())
	}

	col.mu.Lock()
	t.Logf("stop reason: %s", col.stopReason)
	col.mu.Unlock()

	full := col.text()
	t.Logf("full agent response:\n%s", full)
	require.Contains(t, full, "Madrid")
}

// TestCloseConversationFansClosed verifies that closing a conversation emits a
// ConversationClosed event to subscribers so they can finalize per-conversation
// state (the persister relies on this to mark sessions complete).
// TestWaitReadyUnblocksOnHandshakeError verifies that an errored response to a
// handshake request (initialize / session/new) unblocks WaitReady with the
// error, instead of leaving it hung forever waiting for a SessionID that will
// never be assigned.
func TestWaitReadyUnblocksOnHandshakeError(t *testing.T) {
	rt := New()
	meta := types.ConversationMeta{ConversationID: "conv-h"}
	state := &SessionState{meta: meta, ready: make(chan struct{})}
	rt.mu.Lock()
	rt.sessions[meta.ConversationID] = state
	rt.mu.Unlock()

	go rt.onMessage(context.Background(), state, nil, acp.ResponseError{
		Method: acp.AgentMethodSessionNew,
		Err:    acp.NewInternalError(map[string]any{"message": "boom"}),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := rt.WaitReady(ctx, meta)
	require.Error(t, err, "WaitReady must return the handshake error, not block")
	require.NotErrorIs(t, err, context.DeadlineExceeded, "WaitReady must not time out")
}

func TestCloseConversationFansClosed(t *testing.T) {
	rt := New()
	meta := types.ConversationMeta{ConversationID: "conv-x", SessionID: acp.SessionId("sess-x")}

	// Seed a session directly; Create would spawn a subprocess.
	rt.mu.Lock()
	rt.sessions[meta.ConversationID] = &SessionState{meta: meta}
	rt.mu.Unlock()

	var got []types.ConversationClosed
	var mu sync.Mutex
	rt.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
		if c, ok := msg.(types.ConversationClosed); ok {
			mu.Lock()
			got = append(got, c)
			mu.Unlock()
		}
	})

	rt.CloseConversation(meta)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 1)
	require.Equal(t, meta.SessionID, got[0].Meta.SessionID)
}

// TestCreateErrorFansStillbornConversation verifies CreateError records a
// conversation with no subprocess: it fans ConversationCreated, then an
// agent_message_chunk carrying the _meta.acpp error marker and the failure text,
// then an errored ConversationClosed — in that order — and drops the conversation.
func TestCreateErrorFansStillbornConversation(t *testing.T) {
	rt := New()

	var mu sync.Mutex
	var order []string
	var errText string
	var errMarked bool
	var closedErr string
	rt.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
		mu.Lock()
		defer mu.Unlock()
		switch m := msg.(type) {
		case types.ConversationCreated:
			order = append(order, "created")
		case acp.SessionNotification:
			order = append(order, "notification")
			if c := m.Update.AgentMessageChunk; c != nil {
				if c.Content.Text != nil {
					errText = c.Content.Text.Text
				}
				if acpp, ok := c.Meta["acpp"].(map[string]any); ok && acpp["type"] == "error" {
					errMarked = true
				}
			}
		case types.ConversationClosed:
			order = append(order, "closed")
			closedErr = m.Err
		}
	})

	meta := rt.CreateError(context.Background(), types.SessionOpts{ProjectID: "p", Source: "web"}, "boom")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"created", "notification", "closed"}, order)
	require.True(t, errMarked, "error chunk must carry _meta.acpp.type=error")
	require.Equal(t, "boom", errText)
	require.Equal(t, "boom", closedErr)
	require.Empty(t, string(meta.SessionID), "stillborn conversation has no ACP session id")
	require.NotEmpty(t, meta.ConversationID)

	// The conversation must not linger in the router.
	rt.mu.RLock()
	_, ok := rt.sessions[meta.ConversationID]
	rt.mu.RUnlock()
	require.False(t, ok, "stillborn conversation should be dropped after close")
}

// TestConversationFinalizedOnSubprocessExit verifies that when an agent
// subprocess exits on its own (crash, OOM-kill, quitting mid-turn without
// sending a session/prompt response) the router finalizes the conversation: it
// fans an errored ConversationClosed and drops the conversation so it is no
// longer Active. Without this a scheduled job that started the conversation
// would wait forever for a PromptResponse that can never arrive.
func TestConversationFinalizedOnSubprocessExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := New()
	defer rt.Close()

	got := make(chan types.ConversationClosed, 1)
	rt.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
		if c, ok := msg.(types.ConversationClosed); ok {
			select {
			case got <- c:
			default:
			}
		}
	})

	// A fake agent that lives briefly (so Create registers the conversation) then
	// exits without ever speaking ACP: no PromptResponse will ever arrive.
	id, err := rt.Create(ctx, types.SessionOpts{
		ProjectID: "t",
		Agent:     "/bin/sh -c 'sleep 0.2'",
		CWD:       "/",
	})
	require.NoError(t, err)
	require.True(t, rt.Active(id.ConversationID))

	select {
	case c := <-got:
		require.Equal(t, id.ConversationID, c.Meta.ConversationID)
		require.NotEmpty(t, c.Err, "a subprocess-exit close must be marked as an error")
	case <-ctx.Done():
		t.Fatal("router did not fan ConversationClosed after the subprocess exited")
	}

	require.False(t, rt.Active(id.ConversationID),
		"conversation should be dropped once its subprocess exits")
}
