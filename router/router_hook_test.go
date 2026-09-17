package router

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
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

// --- resolveProject (centralized project config resolution) -----------------

// seedProject returns a router backed by an in-memory store carrying the given
// project fields, plus the project name for SessionOpts.ProjectID.
func seedProject(t *testing.T, cfg *config.Config, fields map[string]string) (*Router, string) {
	t.Helper()
	store := db.NewMemStore()
	const name = "widgets"
	for field, value := range fields {
		require.NoError(t, store.SetProjectField(context.Background(), name, field, value))
	}
	opts := []Option{WithProjects(store)}
	if cfg != nil {
		opts = append(opts, WithConfig(cfg))
	}
	return New(opts...), name
}

func TestResolveProject_CallerAgentUsedWithoutStoredAgent(t *testing.T) {
	rt, project := seedProject(t, &config.Config{Defaults: config.Defaults{Agent: "default-agent"}}, nil)
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "caller-agent"}

	hooks, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Empty(t, hooks)
	require.Equal(t, "caller-agent", opts.Agent)
}

func TestResolveProject_StoredAgentOverridesCaller(t *testing.T) {
	rt, project := seedProject(t, nil, map[string]string{"agent": "project-agent"})
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "caller-agent"}

	_, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Equal(t, "project-agent", opts.Agent)
}

func TestResolveProject_FallsBackToConfigDefaultAgent(t *testing.T) {
	rt, project := seedProject(t, &config.Config{Defaults: config.Defaults{Agent: "default-agent"}}, nil)
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project}

	_, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Equal(t, "default-agent", opts.Agent)
}

// A router with no project store (acpp cat, acpp run) resolves from the caller's
// opts and the global config alone.
func TestResolveProject_NoProjectStore(t *testing.T) {
	rt := New(WithConfig(&config.Config{Defaults: config.Defaults{Agent: "default-agent"}}))
	t.Cleanup(rt.Close)
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: "widgets"}

	hooks, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Empty(t, hooks)
	require.Equal(t, "default-agent", opts.Agent)
}

// An empty ProjectID must not trigger a store lookup that would create a row
// named "".
func TestResolveProject_EmptyProjectIDSkipsLookup(t *testing.T) {
	store := db.NewMemStore()
	rt := New(WithProjects(store))
	t.Cleanup(rt.Close)
	opts := types.SessionOpts{CWD: t.TempDir(), Agent: "caller-agent"}

	_, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Equal(t, "caller-agent", opts.Agent)

	projects, err := store.ListProjects(context.Background())
	require.NoError(t, err)
	require.Empty(t, projects)
}

func TestResolveProject_BuildsHooksFromProjectRow(t *testing.T) {
	rt, project := seedProject(t, nil, map[string]string{"hooks": "commit"})
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "x"}

	hooks, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	require.IsType(t, &hook.CommitHook{}, hooks[0])
}

func TestResolveProject_BuildsParameterisedHookFromProjectRow(t *testing.T) {
	rt, project := seedProject(t, nil, map[string]string{"hooks": "worktree:location=/tmp/wt"})
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "x"}

	// That the location param actually reaches the hook is covered by
	// hook.TestWorktreeHookRegistered, where the field is visible; here it is
	// enough that the compact "type:key=value" syntax parses and builds.
	hooks, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	require.IsType(t, &hook.WorktreeHook{}, hooks[0])
}

func TestResolveProject_UnknownHookTypeErrors(t *testing.T) {
	rt, project := seedProject(t, nil, map[string]string{"hooks": "does-not-exist"})
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "x"}

	_, _, err := rt.resolveProject(context.Background(), &opts)
	require.Error(t, err)
}

func TestResolveProject_MalformedHookListErrors(t *testing.T) {
	rt, project := seedProject(t, nil, map[string]string{"hooks": "worktree:location"})
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "x"}

	_, _, err := rt.resolveProject(context.Background(), &opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hooks")
}

// Global hooks from ~/.config/acpp/config.yaml apply to every conversation, even
// a project with no hooks of its own.
func TestResolveProject_BuildsHooksFromGlobalConfig(t *testing.T) {
	rt, project := seedProject(t, &config.Config{
		Hooks: []config.HookConfig{{Type: "worktree"}},
	}, nil)
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "x"}

	hooks, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	require.IsType(t, &hook.WorktreeHook{}, hooks[0])
}

// Global and project hooks concatenate, global first then project.
func TestResolveProject_GlobalAndProjectHooksConcatenate(t *testing.T) {
	rt, project := seedProject(t, &config.Config{
		Hooks: []config.HookConfig{{Type: "worktree"}},
	}, map[string]string{"hooks": "commit"})
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "x"}

	hooks, _, err := rt.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.Len(t, hooks, 2)
	require.IsType(t, &hook.WorktreeHook{}, hooks[0], "global hook must run first")
	require.IsType(t, &hook.CommitHook{}, hooks[1], "project hook must run after")
}

// An unknown hook type in the global config fails loudly, same as a project one.
func TestResolveProject_UnknownGlobalHookTypeErrors(t *testing.T) {
	rt, project := seedProject(t, &config.Config{
		Hooks: []config.HookConfig{{Type: "does-not-exist"}},
	}, nil)
	opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, Agent: "x"}

	_, _, err := rt.resolveProject(context.Background(), &opts)
	require.Error(t, err)
}
