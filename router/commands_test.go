package router

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// seedShellSession registers a conversation directly (without spawning a
// subprocess) so the bang-command path can be exercised in isolation.
func seedShellSession(t *testing.T, rt *Router, opts types.SessionOpts) types.ConversationMeta {
	t.Helper()
	meta := types.ConversationMeta{ConversationID: "conv-shell", SessionID: acp.SessionId("sess-shell")}
	rt.mu.Lock()
	rt.sessions[meta.ConversationID] = &SessionState{meta: meta, opts: opts}
	rt.mu.Unlock()
	return meta
}

// TestHandleShellRunsCommand verifies that a single-line input prefixed with "!"
// is executed and its stdout is returned as feedback — not fanned to subscribers
// (command output is transient, never persisted).
func TestHandleShellRunsCommand(t *testing.T) {
	rt := New()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: cwd})

	col := &collector{t: t, done: make(chan struct{})}
	rt.Subscribe(col.Receive)

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "!echo hello-from-sandbox")
	require.NoError(t, err)
	require.True(t, handled)
	require.Contains(t, feedback, "hello-from-sandbox")
	require.Empty(t, col.text(), "shell output must not be fanned to subscribers")
}

// TestHandleShellRunsInCwd verifies the command runs in the conversation's
// working directory, the same cwd the ACP process was launched with.
func TestHandleShellRunsInCwd(t *testing.T) {
	rt := New()
	dir := t.TempDir()
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: dir})

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "!pwd")
	require.NoError(t, err)
	require.True(t, handled)
	// macOS reports /private/var symlinks for TempDir; match on the suffix.
	require.Contains(t, feedback, strings.TrimPrefix(dir, "/private"))
}

// TestHandleShellReportsFailure verifies a non-zero exit status surfaces to the
// user rather than being swallowed.
func TestHandleShellReportsFailure(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: os.TempDir()})

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "!exit 3")
	require.NoError(t, err)
	require.True(t, handled)
	require.Contains(t, feedback, "exit")
}

// TestHandleShellEmptyCommand verifies a bare "!" is handled (consumed) but runs
// nothing and returns no feedback.
func TestHandleShellEmptyCommand(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: os.TempDir()})

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "!   ")
	require.NoError(t, err)
	require.True(t, handled)
	require.Empty(t, feedback)
}

// TestHandleShellMultiLineIsNotCommand verifies that a multi-line input starting
// with "!" is NOT treated as a shell command — the bang escape is single-line
// only, so the text falls through to be handled as a normal prompt.
func TestHandleShellMultiLineIsNotCommand(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: os.TempDir()})

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "!echo hi\nsecond line")
	require.NoError(t, err)
	require.False(t, handled)
	require.Empty(t, feedback)
}

// TestHandleShellUnknownConversation verifies the command is still consumed
// (handled) but reports an error for a conversation the router does not know.
func TestHandleShellUnknownConversation(t *testing.T) {
	rt := New()
	unknown := types.ConversationMeta{ConversationID: "nope"}

	handled, _, err := rt.HandleCommands(context.Background(), unknown, "!echo hi")
	require.True(t, handled)
	require.Error(t, err)
}

// TestHandleHelpListsHarnessCommands verifies /help returns a listing that
// includes every built-in harness command and, when the agent has advertised
// none, omits the agent section entirely. The listing is returned as feedback,
// not fanned to subscribers.
func TestHandleHelpListsHarnessCommands(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{})

	col := &collector{t: t, done: make(chan struct{})}
	rt.Subscribe(col.Receive)

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "/help")
	require.NoError(t, err)
	require.True(t, handled)
	require.Empty(t, col.text(), "help listing must not be fanned to subscribers")

	require.Contains(t, feedback, "Harness commands:")
	for _, name := range []string{"/cancel", "/clear", "/exit", "/help", "!"} {
		require.Contains(t, feedback, name)
	}
	require.NotContains(t, feedback, "Agent commands:")
}

// TestHandleHelpListsAgentCommandsFirst verifies /help lists the agent's
// advertised commands before the harness commands.
func TestHandleHelpListsAgentCommandsFirst(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{})
	rt.mu.Lock()
	rt.sessions[meta.ConversationID].availableCommands = []acp.AvailableCommand{
		{Name: "review", Description: "Review a pull request"},
		{Name: "plan", Description: "Draft an implementation plan"},
	}
	rt.mu.Unlock()

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "/help")
	require.NoError(t, err)
	require.True(t, handled)

	require.Contains(t, feedback, "Agent commands:")
	require.Contains(t, feedback, "review")
	require.Contains(t, feedback, "Review a pull request")
	require.Contains(t, feedback, "plan")
	require.Less(t, strings.Index(feedback, "Agent commands:"), strings.Index(feedback, "Harness commands:"),
		"agent commands should be listed before harness commands")
}

// TestHandleExitReturnsFeedback verifies /exit invokes the registered shutdown
// hook and returns a confirmation as feedback.
func TestHandleExitReturnsFeedback(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{})
	called := false
	rt.OnShutdown(func() { called = true })

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "/exit")
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, called, "shutdown hook must be invoked")
	require.NotEmpty(t, feedback)
}

// TestHandleUnknownSlashIsNotCommand verifies an unrecognised slash command
// (e.g. an agent-advertised command like /review) is NOT consumed by the
// harness — it falls through to be sent to the agent as a prompt.
func TestHandleUnknownSlashIsNotCommand(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{})

	handled, feedback, err := rt.HandleCommands(context.Background(), meta, "/review the PR")
	require.NoError(t, err)
	require.False(t, handled)
	require.Empty(t, feedback)
}

// TestIsCommand verifies the predicate recognises exactly the inputs the harness
// consumes: leading-slash harness commands and single-line "!" shell escapes.
func TestIsCommand(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"/clear", true},
		{"/exit", true},
		{"/cancel", true},
		{"/help", true},
		{"  /clear  ", true},
		{"!ls", true},
		{"!echo hi\nmore", false}, // multi-line bang is not a shell escape
		{"/review the PR", false}, // agent command, sent as prompt
		{"hello world", false},
		{"", false},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, IsCommand(c.text), "IsCommand(%q)", c.text)
	}
}

// TestOnMessageCapturesAvailableCommands verifies that an available_commands_update
// notification is stored on the conversation state so /help can list it later.
func TestOnMessageCapturesAvailableCommands(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{})

	rt.onMessage(context.Background(), meta.ConversationID, nil, acp.SessionNotification{
		SessionId: meta.SessionID,
		Update: acp.SessionUpdate{
			AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
				AvailableCommands: []acp.AvailableCommand{{Name: "deploy", Description: "Ship it"}},
				SessionUpdate:     "available_commands_update",
			},
		},
	})

	rt.mu.RLock()
	got := rt.sessions[meta.ConversationID].availableCommands
	rt.mu.RUnlock()
	require.Len(t, got, 1)
	require.Equal(t, "deploy", got[0].Name)
}

// TestRunInSandboxNilSandbox verifies the helper runs a command unwrapped when
// no sandbox is configured, returning combined output.
func TestRunInSandboxNilSandbox(t *testing.T) {
	out, err := runInSandbox(context.Background(), nil, os.TempDir(), nil, "echo combined && echo err 1>&2")
	require.NoError(t, err)
	require.Contains(t, out, "combined")
	require.Contains(t, out, "err")
}
