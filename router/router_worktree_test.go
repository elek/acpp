package router

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/hook"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// initGitRepo creates a git repo with one commit and returns its path.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o644))
	run("add", "README")
	run("commit", "-m", "init")
	return dir
}

// worktreeRouter returns a router whose "worktree" project has the worktree hook
// enabled in its stored config, plus that project's name.
func worktreeRouter(t *testing.T) (*Router, string) {
	t.Helper()
	store := db.NewMemStore()
	const project = "worktree-test"
	require.NoError(t, store.SetProjectField(context.Background(), project, "hooks", "worktree"))
	return New(WithProjects(store)), project
}

// TestWorktreeHookContentionEndToEnd drives the worktree hook through the real
// Router.Create path with a lightweight agent that just drains stdin (so no ACP
// handshake is needed and Close is prompt). It verifies the first session uses
// the repo directly, the second (contended) session is redirected to an isolated
// worktree with the repo RW-bound, and the worktree is removed on close.
func TestWorktreeHookContentionEndToEnd(t *testing.T) {
	repo := initGitRepo(t)
	rt, project := worktreeRouter(t)
	t.Cleanup(rt.Close)
	ctx := context.Background()
	// Drains stdin and exits on EOF: stays alive while the session is open, closes
	// promptly when Close shuts stdin. No output, so no ACP parsing noise.
	const agent = `sh -c "cat >/dev/null"`

	// First session: no contention, uses the repo directly.
	m1, err := rt.Create(ctx, types.SessionOpts{CWD: repo, ProjectID: project, Agent: agent})
	require.NoError(t, err)
	o1, ok := rt.Opts(m1.ConversationID)
	require.True(t, ok)
	require.Equal(t, repo, o1.CWD, "first session must use the repo directly")
	require.Empty(t, o1.RWBinds)
	require.Equal(t, 1, rt.runningSessionsForDir(repo))
	require.NoDirExists(t, filepath.Join(repo, ".worktree"))

	// Second session on the same repo: contention -> isolated worktree.
	m2, err := rt.Create(ctx, types.SessionOpts{CWD: repo, ProjectID: project, Agent: agent})
	require.NoError(t, err)
	o2, ok := rt.Opts(m2.ConversationID)
	require.True(t, ok)
	wt := filepath.Join(repo, ".worktree", m2.ConversationID)
	require.Equal(t, wt, o2.CWD, "second session must be redirected to a worktree")
	require.Equal(t, []string{repo}, o2.RWBinds, "original repo must be RW-bound")
	require.DirExists(t, wt)
	// baseDir tracking: the worktree'd session still counts toward the repo.
	require.Equal(t, 2, rt.runningSessionsForDir(repo))

	// Closing the second session removes its worktree and drops the count.
	rt.CloseConversation(m2)
	require.NoDirExists(t, wt)
	require.Equal(t, 1, rt.runningSessionsForDir(repo))

	// Closing the first session (which never got a worktree) is clean.
	rt.CloseConversation(m1)
	require.Equal(t, 0, rt.runningSessionsForDir(repo))
}

// dirtyWorktreeRouter starts two sessions on one repo so the second lands in an
// isolated worktree, then leaves an uncommitted file in that worktree. It
// returns the router, both conversations and the worktree path.
func dirtyWorktreeRouter(t *testing.T) (rt *Router, m1, m2 types.ConversationMeta, wt string) {
	t.Helper()
	repo := initGitRepo(t)
	rt, project := worktreeRouter(t)
	t.Cleanup(rt.Close)
	ctx := context.Background()
	const agent = `sh -c "cat >/dev/null"`

	m1, err := rt.Create(ctx, types.SessionOpts{CWD: repo, ProjectID: project, Agent: agent})
	require.NoError(t, err)
	m2, err = rt.Create(ctx, types.SessionOpts{CWD: repo, ProjectID: project, Agent: agent})
	require.NoError(t, err)

	wt = filepath.Join(repo, ".worktree", m2.ConversationID)
	require.DirExists(t, wt)
	require.NoError(t, os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("wip"), 0o644))
	return rt, m1, m2, wt
}

// A stop the user asked for is refused while the session's worktree holds
// uncommitted work, and the session is left fully alive — not half torn down.
func TestTryCloseConversationRefusedByDirtyWorktree(t *testing.T) {
	rt, _, m2, wt := dirtyWorktreeRouter(t)

	err := rt.TryCloseConversation(m2)

	var refused *hook.CloseRefusedError
	require.ErrorAs(t, err, &refused, "a dirty worktree must refuse the close")
	require.Contains(t, refused.Reason, "notes.txt", "the refusal must say what would be lost")
	require.DirExists(t, wt, "the worktree must be untouched")
	require.FileExists(t, filepath.Join(wt, "notes.txt"))
	require.True(t, rt.Active(m2.ConversationID), "the refused session must stay live")
	_, ok := rt.Opts(m2.ConversationID)
	require.True(t, ok, "the refused session must stay registered")
}

// With nothing to lose, the same stop goes through and cleans up as before.
func TestTryCloseConversationClosesCleanWorktree(t *testing.T) {
	repo := initGitRepo(t)
	rt, project := worktreeRouter(t)
	t.Cleanup(rt.Close)
	ctx := context.Background()
	const agent = `sh -c "cat >/dev/null"`

	_, err := rt.Create(ctx, types.SessionOpts{CWD: repo, ProjectID: project, Agent: agent})
	require.NoError(t, err)
	m2, err := rt.Create(ctx, types.SessionOpts{CWD: repo, ProjectID: project, Agent: agent})
	require.NoError(t, err)
	wt := filepath.Join(repo, ".worktree", m2.ConversationID)
	require.DirExists(t, wt)

	require.NoError(t, rt.TryCloseConversation(m2))
	require.NoDirExists(t, wt, "a clean worktree is removed on close as before")
	require.False(t, rt.Active(m2.ConversationID))
}

// "Stop anyway" overrules the veto and tears the worktree down.
func TestForceCloseConversationRemovesDirtyWorktree(t *testing.T) {
	rt, _, m2, wt := dirtyWorktreeRouter(t)

	rt.ForceCloseConversation(m2)

	require.NoDirExists(t, wt, "a forced close must remove the worktree")
	require.False(t, rt.Active(m2.ConversationID))
}

// Shutdown has nobody to ask, so it leaves uncommitted work on disk rather than
// destroying it unobserved.
func TestCloseKeepsDirtyWorktreeOnShutdown(t *testing.T) {
	rt, _, _, wt := dirtyWorktreeRouter(t)

	rt.Close()

	require.DirExists(t, wt, "shutdown must not destroy uncommitted work")
	require.FileExists(t, filepath.Join(wt, "notes.txt"))
}

// The unguarded CloseConversation (scheduler, arena, tck) is likewise
// non-destructive: it closes the session but keeps the dirty worktree.
func TestCloseConversationKeepsDirtyWorktree(t *testing.T) {
	rt, _, m2, wt := dirtyWorktreeRouter(t)

	rt.CloseConversation(m2)

	require.False(t, rt.Active(m2.ConversationID), "the session is closed")
	require.DirExists(t, wt, "but its uncommitted work is kept")
}

// A conversation with no worktree at all is closed without a murmur.
func TestTryCloseConversationWithoutWorktree(t *testing.T) {
	rt, m1, _, _ := dirtyWorktreeRouter(t)

	require.NoError(t, rt.TryCloseConversation(m1), "the repo session has no worktree to guard")
	require.False(t, rt.Active(m1.ConversationID))
}

// TestWorktreeHookAnnouncesWorktree verifies that when the worktree hook
// redirects a contended session into an isolated worktree, the router emits a
// first harness message naming the worktree directory, and that an uncontended
// (first) session produces no such notice.
func TestWorktreeHookAnnouncesWorktree(t *testing.T) {
	repo := initGitRepo(t)
	rt, project := worktreeRouter(t)
	t.Cleanup(rt.Close)
	ctx := context.Background()
	const agent = `sh -c "cat >/dev/null"`

	// Collect the text of every harness worktree notice, keyed by conversation.
	var notices []string
	rt.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
		n, ok := msg.(acp.SessionNotification)
		if !ok || n.Update.AgentMessageChunk == nil {
			return
		}
		acpp, _ := n.Update.AgentMessageChunk.Meta["acpp"].(map[string]any)
		if acpp["type"] != "notice" {
			return
		}
		if txt := n.Update.AgentMessageChunk.Content.Text; txt != nil {
			notices = append(notices, txt.Text)
		}
	})

	// First session: no contention, no worktree, no notice.
	m1, err := rt.Create(ctx, types.SessionOpts{CWD: repo, ProjectID: project, Agent: agent})
	require.NoError(t, err)
	require.Empty(t, notices, "first session must not announce a worktree")

	// Second session: contention -> worktree -> a notice naming the worktree dir.
	m2, err := rt.Create(ctx, types.SessionOpts{CWD: repo, ProjectID: project, Agent: agent})
	require.NoError(t, err)
	wt := filepath.Join(repo, ".worktree", m2.ConversationID)
	require.Len(t, notices, 1, "contended session must announce its worktree once")
	require.Contains(t, notices[0], wt, "notice must name the worktree directory")

	rt.CloseConversation(m2)
	rt.CloseConversation(m1)
}
