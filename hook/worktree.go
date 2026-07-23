package hook

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/elek/acpp/types"
)

func init() {
	Register("worktree", func(params map[string]string) (Hook, error) {
		location := params["location"]
		if location == "" {
			location = defaultWorktreeLocation
		}
		return NewWorktreeHook(location), nil
	})
}

const defaultWorktreeLocation = ".worktree"

// WorktreeHook redirects a session's working directory to an isolated git
// worktree when its target directory is a git repo that ALREADY has a running
// session. The first session on a repo uses the repo directly; each subsequent
// concurrent session gets its own worktree (on a new branch named after the
// conversation id) so live agents don't step on one another's working tree. The
// original repo is bind-mounted read-write so git metadata works and commits
// land in the real repo; the worktree is removed when the session stops.
//
// It carries no per-conversation state and implements Outgoing/Incoming as
// no-ops, so it lives in the same .acpp.yaml hooks list as message hooks.
type WorktreeHook struct {
	location string
}

// NewWorktreeHook returns a WorktreeHook placing worktrees under location
// (relative to the repo root, or an absolute path).
func NewWorktreeHook(location string) *WorktreeHook {
	return &WorktreeHook{location: location}
}

func (h *WorktreeHook) Outgoing(hc HookContext, msg any) any { return msg }
func (h *WorktreeHook) Incoming(hc HookContext, msg any) any { return msg }

// SetupSession creates and wires up an isolated worktree when the session's
// target directory is a contended git repo. See WorktreeHook.
func (h *WorktreeHook) SetupSession(sc SessionContext, opts *types.SessionOpts) (func(), error) {
	repo := opts.CWD

	// Only act on a git repository.
	if !isGitRepo(repo) {
		return nil, nil
	}
	// Only act on contention: a session must already be running on this repo.
	if sc.RunningSessionsForDir == nil || sc.RunningSessionsForDir(repo) == 0 {
		return nil, nil
	}

	wt := h.worktreePath(repo, sc.ConversationID)
	if err := gitWorktreeAdd(repo, sc.ConversationID, wt); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}

	// Keep the worktree location out of the parent's untracked list (best-effort).
	if err := appendGitExclude(repo, h.location); err != nil {
		slog.Warn("worktree hook: could not update .git/info/exclude", "repo", repo, "err", err)
	}

	// Redirect the session into the worktree; expose the original repo read-write.
	opts.CWD = wt
	opts.RWBinds = append(opts.RWBinds, repo)

	slog.Info("worktree hook: redirected session to isolated worktree",
		"repo", repo, "worktree", wt, "branch", sc.ConversationID)

	cleanup := func() {
		if err := gitWorktreeRemove(repo, wt); err != nil {
			slog.Warn("worktree hook: remove failed", "worktree", wt, "err", err)
		}
	}
	return cleanup, nil
}

// worktreePath returns the worktree directory for a conversation: <location>/<id>,
// with a relative location joined to the repo root.
func (h *WorktreeHook) worktreePath(repo, convID string) string {
	loc := h.location
	if !filepath.IsAbs(loc) {
		loc = filepath.Join(repo, loc)
	}
	return filepath.Join(loc, convID)
}

// isGitRepo reports whether dir contains a .git entry (dir or file).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitWorktreeAdd creates a worktree at path on a new branch from current HEAD.
func gitWorktreeAdd(repo, branch, path string) error {
	return runGit(repo, "worktree", "add", "-b", branch, path)
}

// gitWorktreeRemove force-removes the worktree at path, keeping its branch.
func gitWorktreeRemove(repo, path string) error {
	return runGit(repo, "worktree", "remove", "--force", path)
}

// runGit runs `git -C repo args...`, returning combined output on error.
func runGit(repo string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// appendGitExclude adds entry to <repo>/.git/info/exclude if not already present.
func appendGitExclude(repo, entry string) error {
	path := filepath.Join(repo, ".git", "info", "exclude")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil // already excluded
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + entry + "\n")
	return err
}
