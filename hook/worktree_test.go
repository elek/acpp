package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elek/acpp/config"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// initRepo creates a git repo with one commit in a fresh temp dir and returns it.
func initRepo(t *testing.T) string {
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

// branchExists reports whether branch exists in the repo.
func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "refs/heads/"+branch)
	return cmd.Run() == nil
}

// worktreeExists reports whether path is registered as a worktree of repo.
func worktreeExists(t *testing.T, repo, path string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "worktree", "list").Output()
	require.NoError(t, err)
	return strings.Contains(string(out), path)
}

func TestWorktreeHookNonGitDirIsNoOp(t *testing.T) {
	dir := t.TempDir() // not a git repo
	h := NewWorktreeHook(".worktree")
	opts := &types.SessionOpts{CWD: dir}
	sc := SessionContext{
		ConversationID:        "conv1",
		BaseDir:               dir,
		RunningSessionsForDir: func(string) int { return 5 },
	}

	cleanup, err := h.SetupSession(sc, opts)
	require.NoError(t, err)
	require.Nil(t, cleanup)
	require.Equal(t, dir, opts.CWD, "CWD must be untouched for a non-git dir")
	require.Empty(t, opts.RWBinds)
}

func TestWorktreeHookFirstSessionIsNoOp(t *testing.T) {
	repo := initRepo(t)
	h := NewWorktreeHook(".worktree")
	opts := &types.SessionOpts{CWD: repo}
	sc := SessionContext{
		ConversationID:        "conv1",
		BaseDir:               repo,
		RunningSessionsForDir: func(string) int { return 0 }, // no contention
	}

	cleanup, err := h.SetupSession(sc, opts)
	require.NoError(t, err)
	require.Nil(t, cleanup)
	require.Equal(t, repo, opts.CWD, "first session uses the repo directly")
	require.Empty(t, opts.RWBinds)
	require.NoDirExists(t, filepath.Join(repo, ".worktree"))
}

func TestWorktreeHookContentionCreatesWorktree(t *testing.T) {
	repo := initRepo(t)
	h := NewWorktreeHook(".worktree")
	opts := &types.SessionOpts{CWD: repo}
	sc := SessionContext{
		ConversationID:        "conv2",
		BaseDir:               repo,
		RunningSessionsForDir: func(string) int { return 1 }, // one already running
	}

	cleanup, err := h.SetupSession(sc, opts)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	wt := filepath.Join(repo, ".worktree", "conv2")
	require.Equal(t, wt, opts.CWD, "CWD must be redirected to the worktree")
	require.Equal(t, []string{repo}, opts.RWBinds, "original repo must be RW-bound")
	require.DirExists(t, wt)
	require.True(t, worktreeExists(t, repo, wt), "worktree must be registered")
	require.True(t, branchExists(t, repo, "conv2"), "worktree branch must exist")

	// The location is excluded so it doesn't show up as untracked in the parent.
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	require.NoError(t, err)
	require.Contains(t, string(exclude), ".worktree")

	// Teardown removes the worktree but keeps the branch.
	cleanup()
	require.False(t, worktreeExists(t, repo, wt), "worktree must be removed on cleanup")
	require.NoDirExists(t, wt)
	require.True(t, branchExists(t, repo, "conv2"), "branch must survive teardown")
}

func TestWorktreeHookCustomLocation(t *testing.T) {
	repo := initRepo(t)
	h := NewWorktreeHook("trees")
	opts := &types.SessionOpts{CWD: repo}
	sc := SessionContext{
		ConversationID:        "conv3",
		BaseDir:               repo,
		RunningSessionsForDir: func(string) int { return 2 },
	}

	cleanup, err := h.SetupSession(sc, opts)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	defer cleanup()

	require.Equal(t, filepath.Join(repo, "trees", "conv3"), opts.CWD)
}

// The worktree hook still satisfies the base Hook interface (no-op transforms).
func TestWorktreeHookImplementsHook(t *testing.T) {
	var h Hook = NewWorktreeHook(".worktree")
	msg := "x"
	require.Equal(t, msg, h.Outgoing(HookContext{}, msg))
	require.Equal(t, msg, h.Incoming(HookContext{}, msg))
	_, ok := h.(SessionHook)
	require.True(t, ok, "WorktreeHook must implement SessionHook")
}

// The hook is registered under "worktree" and honors the location param.
func TestWorktreeHookRegistered(t *testing.T) {
	hooks, err := Build([]config.HookConfig{
		{Type: "worktree", Params: map[string]string{"location": "wt"}},
	})
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	require.IsType(t, &WorktreeHook{}, hooks[0])
}
