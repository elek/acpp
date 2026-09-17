package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	cleanup(false)
	require.False(t, worktreeExists(t, repo, wt), "worktree must be removed on cleanup")
	require.NoDirExists(t, wt)
	require.True(t, branchExists(t, repo, "conv2"), "branch must survive teardown")
}

// contendedWorktree sets up a worktree'd session and returns the hook, the repo,
// the worktree path and its teardown func.
func contendedWorktree(t *testing.T, convID string) (h *WorktreeHook, repo, wt string, cleanup func(bool)) {
	t.Helper()
	repo = initRepo(t)
	h = NewWorktreeHook(".worktree")
	opts := &types.SessionOpts{CWD: repo}
	sc := SessionContext{
		ConversationID:        convID,
		BaseDir:               repo,
		RunningSessionsForDir: func(string) int { return 1 },
	}
	cleanup, err := h.SetupSession(sc, opts)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	return h, repo, opts.CWD, cleanup
}

// A session that never got a worktree has nothing to lose, so it never vetoes.
func TestWorktreeHookCanCloseWithoutWorktree(t *testing.T) {
	repo := initRepo(t)
	h := NewWorktreeHook(".worktree")
	opts := &types.SessionOpts{CWD: repo}
	sc := SessionContext{
		ConversationID:        "conv1",
		BaseDir:               repo,
		RunningSessionsForDir: func(string) int { return 0 }, // no contention
	}
	_, err := h.SetupSession(sc, opts)
	require.NoError(t, err)

	require.Empty(t, h.CanClose(), "a session with no worktree must never veto a close")
}

// A worktree with nothing to save closes without argument.
func TestWorktreeHookCanCloseCleanWorktree(t *testing.T) {
	h, _, _, cleanup := contendedWorktree(t, "conv2")
	defer cleanup(true)

	require.Empty(t, h.CanClose(), "a clean worktree must not veto a close")
}

// A file the agent wrote but never committed is exactly what must not be lost.
func TestWorktreeHookCanCloseVetoesUntrackedFile(t *testing.T) {
	h, _, wt, cleanup := contendedWorktree(t, "conv2")
	defer cleanup(true)

	require.NoError(t, os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("wip"), 0o644))

	reason := h.CanClose()
	require.NotEmpty(t, reason, "an untracked file must veto the close")
	require.Contains(t, reason, wt, "the reason must name the worktree")
	require.Contains(t, reason, "notes.txt", "the reason must name the changed file")
}

// An edit to a tracked file vetoes just the same.
func TestWorktreeHookCanCloseVetoesModifiedFile(t *testing.T) {
	h, _, wt, cleanup := contendedWorktree(t, "conv2")
	defer cleanup(true)

	require.NoError(t, os.WriteFile(filepath.Join(wt, "README"), []byte("changed"), 0o644))

	reason := h.CanClose()
	require.NotEmpty(t, reason, "a modified tracked file must veto the close")
	require.Contains(t, reason, "README")
}

// Work that is committed to the ephemeral branch is not at risk — the branch
// survives teardown — so it must not block the stop.
func TestWorktreeHookCanCloseIgnoresCommittedWork(t *testing.T) {
	h, _, wt, cleanup := contendedWorktree(t, "conv2")
	defer cleanup(true)

	require.NoError(t, os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("wip"), 0o644))
	gitIn(t, wt, "add", "notes.txt")
	gitIn(t, wt, "commit", "-m", "wip")

	require.Empty(t, h.CanClose(), "committed work must not veto a close")
}

// Routine teardown must never destroy uncommitted work: the directory, its
// registration and its branch all survive so the changes can be recovered.
func TestWorktreeHookCleanupKeepsDirtyWorktree(t *testing.T) {
	h, repo, wt, cleanup := contendedWorktree(t, "conv2")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("wip"), 0o644))

	cleanup(false)

	require.DirExists(t, wt, "a dirty worktree must survive an unforced teardown")
	require.True(t, worktreeExists(t, repo, wt), "it must stay registered so git can still reach it")
	require.True(t, branchExists(t, repo, "conv2"), "branch must survive")
	require.FileExists(t, filepath.Join(wt, "notes.txt"), "the uncommitted file must still be there")
	require.NotEmpty(t, h.CanClose(), "the veto still stands after an unforced teardown")
}

// "Stop anyway" overrules the veto: the worktree goes, the branch stays.
func TestWorktreeHookForcedCleanupRemovesDirtyWorktree(t *testing.T) {
	_, repo, wt, cleanup := contendedWorktree(t, "conv2")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("wip"), 0o644))

	cleanup(true)

	require.NoDirExists(t, wt, "a forced teardown must remove the worktree")
	require.False(t, worktreeExists(t, repo, wt))
	require.True(t, branchExists(t, repo, "conv2"), "branch must survive even a forced teardown")
}

// A clean worktree is removed by routine teardown, as before.
func TestWorktreeHookCleanupRemovesCleanWorktree(t *testing.T) {
	_, repo, wt, cleanup := contendedWorktree(t, "conv2")

	cleanup(false)

	require.NoDirExists(t, wt)
	require.True(t, branchExists(t, repo, "conv2"))
}

// A broken git check must not wedge a session you can never close: if the
// worktree directory is gone from under us, the hook stops vetoing.
func TestWorktreeHookCanCloseSafeWhenGitFails(t *testing.T) {
	h, _, wt, _ := contendedWorktree(t, "conv2")
	require.NoError(t, os.RemoveAll(wt))

	require.Empty(t, h.CanClose(), "an unreadable worktree must not veto the close")
}

// A git that never answers must not hang the stop the user is waiting on: the
// check gives up and lets the close proceed.
func TestWorktreeHookCanCloseGivesUpOnHangingGit(t *testing.T) {
	h, _, _, _ := contendedWorktree(t, "conv2")

	// A "git" that hangs forever, ahead of the real one on PATH.
	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 300\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	done := make(chan string, 1)
	go func() { done <- h.CanClose() }()
	select {
	case reason := <-done:
		require.Empty(t, reason, "a git that cannot answer must not veto the close")
	case <-time.After(gitTimeout + 5*time.Second):
		t.Fatal("CanClose hung on an unresponsive git")
	}
}

// gitIn runs a git command inside dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
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
	defer cleanup(false)

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
	_, ok = h.(CloseGuard)
	require.True(t, ok, "WorktreeHook must implement CloseGuard")
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
