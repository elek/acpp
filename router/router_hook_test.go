package router

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/hook"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// --- test hooks -------------------------------------------------------------

// rewriteHook replaces every outgoing PromptRequest's text with "REWRITTEN".
type rewriteHook struct{}

func (rewriteHook) Outgoing(hc hook.HookContext, msg any) any {
	if req, ok := msg.(acp.PromptRequest); ok {
		req.Prompt = []acp.ContentBlock{acp.TextBlock("REWRITTEN")}
		return req
	}
	return msg
}
func (rewriteHook) Incoming(hc hook.HookContext, msg any) any { return msg }

// dropHook drops incoming SessionNotifications.
type dropHook struct{}

func (dropHook) Outgoing(hc hook.HookContext, msg any) any { return msg }
func (dropHook) Incoming(hc hook.HookContext, msg any) any {
	if _, ok := msg.(acp.SessionNotification); ok {
		return nil
	}
	return msg
}

// triggerHook injects a follow-up prompt when a turn's PromptResponse arrives.
type triggerHook struct{ prompt string }

func (triggerHook) Outgoing(hc hook.HookContext, msg any) any { return msg }
func (h triggerHook) Incoming(hc hook.HookContext, msg any) any {
	if _, ok := msg.(acp.PromptResponse); ok {
		_ = hc.Trigger(h.prompt)
	}
	return msg
}

// seedSession installs a SessionState with a usable (but inert) connection so
// dispatch/Send can write to the agent without a real subprocess: outbound bytes
// are discarded and the inbound pipe blocks until the test ends.
func seedSession(t *testing.T, rt *Router, hooks []hook.Hook) (*SessionState, types.ConversationMeta) {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })

	meta := types.ConversationMeta{ConversationID: "c1", SessionID: acp.SessionId("s1")}
	conn := acp.NewClientSideConnection(func(context.Context, *json.RawMessage, any) {}, io.Discard, pr)
	st := &SessionState{
		meta:       meta,
		connection: conn,
		hooks:      hooks,
		opts:       types.SessionOpts{CWD: t.TempDir()},
	}
	rt.mu.Lock()
	rt.sessions[meta.ConversationID] = st
	rt.mu.Unlock()
	return st, meta
}

func promptTextOf(msg any) (string, bool) {
	req, ok := msg.(acp.PromptRequest)
	if !ok {
		return "", false
	}
	var s string
	for _, b := range req.Prompt {
		if b.Text != nil {
			s += b.Text.Text
		}
	}
	return s, true
}

// --- Outgoing ---------------------------------------------------------------

func TestSend_OutgoingHookRewritesMessage(t *testing.T) {
	rt := New()
	_, meta := seedSession(t, rt, []hook.Hook{rewriteHook{}})

	var got []any
	var mu sync.Mutex
	rt.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	})

	err := rt.Send(context.Background(), meta, acp.PromptRequest{
		SessionId: "s1",
		Prompt:    []acp.ContentBlock{acp.TextBlock("original")},
	})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 1)
	text, ok := promptTextOf(got[0])
	require.True(t, ok)
	require.Equal(t, "REWRITTEN", text)
}

// --- Incoming drop ----------------------------------------------------------

func TestDeliver_IncomingHookDropsMessage(t *testing.T) {
	rt := New()
	st, meta := seedSession(t, rt, []hook.Hook{dropHook{}})

	var got []any
	var mu sync.Mutex
	rt.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	})

	rt.deliver(context.Background(), st, nil, meta, acp.SessionNotification{SessionId: "s1"})

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, got, "dropped message should not reach subscribers")
}

// --- Trigger ordering -------------------------------------------------------

func TestDeliver_TriggerDeferredUntilAfterDelivery(t *testing.T) {
	rt := New()
	st, meta := seedSession(t, rt, []hook.Hook{triggerHook{prompt: "followup"}})

	var order []string
	var mu sync.Mutex
	rt.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
		mu.Lock()
		defer mu.Unlock()
		switch m := msg.(type) {
		case acp.PromptResponse:
			order = append(order, "response")
		case acp.PromptRequest:
			text, _ := promptTextOf(m)
			order = append(order, "prompt:"+text)
		}
	})

	rt.deliver(context.Background(), st, nil, meta, acp.PromptResponse{StopReason: acp.StopReasonEndTurn})

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"response", "prompt:followup"}, order,
		"the follow-up prompt must be fanned out only after the triggering message")
}

// --- resolveProject (centralized .acpp.yaml resolution) ---------------------

func writeProject(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, config.ProjectFile), []byte(content), 0o644))
}

func TestResolveProject_CallerAgentUsedWithoutProjectFile(t *testing.T) {
	rt := New(WithConfig(&config.Config{Defaults: config.Defaults{Agent: "default-agent"}}))
	opts := types.SessionOpts{CWD: t.TempDir(), Agent: "caller-agent"}

	hooks, err := rt.resolveProject(&opts)
	require.NoError(t, err)
	require.Empty(t, hooks)
	require.Equal(t, "caller-agent", opts.Agent)
}

func TestResolveProject_ProjectAgentOverridesCaller(t *testing.T) {
	dir := t.TempDir()
	writeProject(t, dir, "agent: project-agent\n")

	rt := New()
	opts := types.SessionOpts{CWD: dir, Agent: "caller-agent"}
	_, err := rt.resolveProject(&opts)
	require.NoError(t, err)
	require.Equal(t, "project-agent", opts.Agent)
}

func TestResolveProject_FallsBackToConfigDefaultAgent(t *testing.T) {
	rt := New(WithConfig(&config.Config{Defaults: config.Defaults{Agent: "default-agent"}}))
	opts := types.SessionOpts{CWD: t.TempDir()}

	_, err := rt.resolveProject(&opts)
	require.NoError(t, err)
	require.Equal(t, "default-agent", opts.Agent)
}

func TestResolveProject_BuildsHooksFromProjectFile(t *testing.T) {
	dir := t.TempDir()
	writeProject(t, dir, "hooks:\n  - type: commit\n")

	rt := New()
	opts := types.SessionOpts{CWD: dir, Agent: "x"}
	hooks, err := rt.resolveProject(&opts)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
}

func TestResolveProject_UnknownHookTypeErrors(t *testing.T) {
	dir := t.TempDir()
	writeProject(t, dir, "hooks:\n  - type: does-not-exist\n")

	rt := New()
	opts := types.SessionOpts{CWD: dir, Agent: "x"}
	_, err := rt.resolveProject(&opts)
	require.Error(t, err)
}
