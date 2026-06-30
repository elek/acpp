package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// TestCommitHook exercises the commit hook end to end: a project configured with
// the commit hook, a dirty working tree, and a substantive prompt should cause
// acpp to inject a follow-up "commit" prompt that the agent (rai acp fake) turns
// into a real git commit.
func TestCommitHook(t *testing.T) {
	WithRouter(t, func(t *testing.T, dir string, r *router.Router) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// A git project with one initial commit.
		proj := filepath.Join(dir, "proj")
		require.NoError(t, os.MkdirAll(proj, 0o755))
		gitInit(t, proj)
		writeFile(t, filepath.Join(proj, "README.md"), "initial\n")
		git(t, proj, "add", "-A")
		git(t, proj, "commit", "-m", "initial commit")
		require.Equal(t, 1, commitCount(proj), "expected exactly the initial commit")

		// Configure the project to run rai acp fake with the commit hook.
		writeFile(t, filepath.Join(proj, ".acpp.yaml"),
			"agent: rai acp fake\nhooks:\n  - type: commit\n")

		// Dirty the working tree by modifying a tracked file. The commit hook only
		// fires when the tree is dirty at end of turn (hook/commit.go), and rai acp
		// fake does not edit files on a normal turn, so the test stages the change.
		writeFile(t, filepath.Join(proj, "README.md"), "initial\nmore work\n")
		require.True(t, isDirty(proj), "tree should be dirty before the prompt")

		// Start a conversation in the project. Agent and hooks come from .acpp.yaml.
		id, err := r.Create(ctx, types.SessionOpts{
			ProjectID: proj,
			CWD:       proj,
			Source:    "test",
		})
		require.NoError(t, err)

		id, err = r.WaitReady(ctx, id)
		require.NoError(t, err)
		require.NotEmpty(t, id.SessionID)

		// The commit hook ignores prompts of 100 characters or fewer, so send a
		// longer one to mark the turn as substantive.
		prompt := strings.Repeat("Please apply the requested change to the project files. ", 3)
		require.Greater(t, len(prompt), 100)

		err = r.Send(ctx, id, acp.PromptRequest{
			SessionId: id.SessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
		})
		require.NoError(t, err)

		// The first turn produces text and leaves the tree dirty; the commit hook
		// then injects a "commit" follow-up that rai acp fake commits. Wait for the
		// new commit to land.
		require.Eventually(t, func() bool {
			return commitCount(proj) == 2
		}, 30*time.Second, 200*time.Millisecond,
			"expected the commit hook to create a second commit")
	})
}

// gitInit initializes a git repository in dir with a deterministic identity so
// commits succeed in environments without global git config.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "test@acpp.test")
	git(t, dir, "config", "user.name", "acpp test")
}

// git runs a git command in dir, failing the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
	return string(out)
}

// commitCount returns the number of commits reachable from HEAD, or -1 on error.
// It takes no *testing.T so it is safe to call from a require.Eventually probe
// goroutine.
func commitCount(dir string) int {
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1
	}
	return n
}

// isDirty reports whether dir has uncommitted changes.
func isDirty(dir string) bool {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// writeFile writes content to path, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
