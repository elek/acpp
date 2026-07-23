package router

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

// TestWorktreeHookContentionEndToEnd drives the worktree hook through the real
// Router.Create path with a lightweight agent that just drains stdin (so no ACP
// handshake is needed and Close is prompt). It verifies the first session uses
// the repo directly, the second (contended) session is redirected to an isolated
// worktree with the repo RW-bound, and the worktree is removed on close.
func TestWorktreeHookContentionEndToEnd(t *testing.T) {
	repo := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".acpp.yaml"),
		[]byte("hooks:\n  - type: worktree\n"), 0o644))

	rt := New()
	t.Cleanup(rt.Close)
	ctx := context.Background()
	// Drains stdin and exits on EOF: stays alive while the session is open, closes
	// promptly when Close shuts stdin. No output, so no ACP parsing noise.
	const agent = `sh -c "cat >/dev/null"`

	// First session: no contention, uses the repo directly.
	m1, err := rt.Create(ctx, types.SessionOpts{CWD: repo, Agent: agent})
	require.NoError(t, err)
	o1, ok := rt.Opts(m1.ConversationID)
	require.True(t, ok)
	require.Equal(t, repo, o1.CWD, "first session must use the repo directly")
	require.Empty(t, o1.RWBinds)
	require.Equal(t, 1, rt.runningSessionsForDir(repo))
	require.NoDirExists(t, filepath.Join(repo, ".worktree"))

	// Second session on the same repo: contention -> isolated worktree.
	m2, err := rt.Create(ctx, types.SessionOpts{CWD: repo, Agent: agent})
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
