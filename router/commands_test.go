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
// is executed and its stdout is fanned back to subscribers as an agent message.
func TestHandleShellRunsCommand(t *testing.T) {
	rt := New()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: cwd})

	col := &collector{t: t, done: make(chan struct{})}
	rt.Subscribe(col.Receive)

	handled, err := rt.HandleCommands(context.Background(), meta, "!echo hello-from-sandbox")
	require.NoError(t, err)
	require.True(t, handled)
	require.Contains(t, col.text(), "hello-from-sandbox")
}

// TestHandleShellRunsInCwd verifies the command runs in the conversation's
// working directory, the same cwd the ACP process was launched with.
func TestHandleShellRunsInCwd(t *testing.T) {
	rt := New()
	dir := t.TempDir()
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: dir})

	col := &collector{t: t, done: make(chan struct{})}
	rt.Subscribe(col.Receive)

	handled, err := rt.HandleCommands(context.Background(), meta, "!pwd")
	require.NoError(t, err)
	require.True(t, handled)
	// macOS reports /private/var symlinks for TempDir; match on the suffix.
	require.Contains(t, col.text(), strings.TrimPrefix(dir, "/private"))
}

// TestHandleShellReportsFailure verifies a non-zero exit status surfaces to the
// user rather than being swallowed.
func TestHandleShellReportsFailure(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: os.TempDir()})

	col := &collector{t: t, done: make(chan struct{})}
	rt.Subscribe(col.Receive)

	handled, err := rt.HandleCommands(context.Background(), meta, "!exit 3")
	require.NoError(t, err)
	require.True(t, handled)
	require.Contains(t, col.text(), "exit")
}

// TestHandleShellEmptyCommand verifies a bare "!" is handled (consumed) but runs
// nothing.
func TestHandleShellEmptyCommand(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: os.TempDir()})

	handled, err := rt.HandleCommands(context.Background(), meta, "!   ")
	require.NoError(t, err)
	require.True(t, handled)
}

// TestHandleShellMultiLineIsNotCommand verifies that a multi-line input starting
// with "!" is NOT treated as a shell command — the bang escape is single-line
// only, so the text falls through to be handled as a normal prompt.
func TestHandleShellMultiLineIsNotCommand(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{CWD: os.TempDir()})

	handled, err := rt.HandleCommands(context.Background(), meta, "!echo hi\nsecond line")
	require.NoError(t, err)
	require.False(t, handled)
}

// TestHandleShellUnknownConversation verifies the command is still consumed
// (handled) but reports an error for a conversation the router does not know.
func TestHandleShellUnknownConversation(t *testing.T) {
	rt := New()
	unknown := types.ConversationMeta{ConversationID: "nope"}

	handled, err := rt.HandleCommands(context.Background(), unknown, "!echo hi")
	require.True(t, handled)
	require.Error(t, err)
}

// TestHandleHelpListsHarnessCommands verifies /help fans back a listing that
// includes every built-in harness command and, when the agent has advertised
// none, omits the agent section entirely.
func TestHandleHelpListsHarnessCommands(t *testing.T) {
	rt := New()
	meta := seedShellSession(t, rt, types.SessionOpts{})

	col := &collector{t: t, done: make(chan struct{})}
	rt.Subscribe(col.Receive)

	handled, err := rt.HandleCommands(context.Background(), meta, "/help")
	require.NoError(t, err)
	require.True(t, handled)

	out := col.text()
	require.Contains(t, out, "Harness commands:")
	for _, name := range []string{"/cancel", "/clear", "/exit", "/help", "!"} {
		require.Contains(t, out, name)
	}
	require.NotContains(t, out, "Agent commands:")
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

	col := &collector{t: t, done: make(chan struct{})}
	rt.Subscribe(col.Receive)

	handled, err := rt.HandleCommands(context.Background(), meta, "/help")
	require.NoError(t, err)
	require.True(t, handled)

	out := col.text()
	require.Contains(t, out, "Agent commands:")
	require.Contains(t, out, "review")
	require.Contains(t, out, "Review a pull request")
	require.Contains(t, out, "plan")
	require.Less(t, strings.Index(out, "Agent commands:"), strings.Index(out, "Harness commands:"),
		"agent commands should be listed before harness commands")
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
