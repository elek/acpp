package hook

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/elek/acpp/process"
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

// gitTimeout bounds the dirty check. It runs while a user waits on a Stop
// click, so an unresponsive git must fail the check rather than hang the stop.
const gitTimeout = 5 * time.Second

// WorktreeHook redirects a session's working directory to an isolated git
// worktree when its target directory is a git repo that ALREADY has a running
// session. The first session on a repo uses the repo directly; each subsequent
// concurrent session gets its own worktree (on a new branch named after the
// conversation id) so live agents don't step on one another's working tree. The
// original repo is bind-mounted read-write so git metadata works and commits
// land in the real repo; the worktree is removed when the session stops.
//
// It implements Outgoing/Incoming as no-ops, so it lives in the same project
// hooks list as message hooks.
//
// Uncommitted work in the worktree is never destroyed without the user saying
// so: the hook is a CloseGuard that vetoes a deliberate close while the worktree
// is dirty, and its teardown removes the worktree only when it is clean or when
// the close was forced. The branch is kept in every case.
type WorktreeHook struct {
	location string
	// repo and wt are the origin repository and the worktree created for this
	// conversation, recorded by SetupSession. Both are empty when no worktree was
	// created (no contention, or not a git repo). Instances are per-conversation
	// and these are written before the conversation is registered with the router,
	// so later reads from close paths need no lock.
	repo   string
	wt     string
	branch string
	// host is the machine the session runs on; nil means this one.
	host process.Host
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
func (h *WorktreeHook) SetupSession(sc SessionContext, opts *types.SessionOpts) (func(bool), error) {
	repo := opts.CWD
	h.host = sc.Host

	// Only act on a git repository.
	if !isGitRepo(h.host, repo) {
		return nil, nil
	}
	// Only act on contention: a session must already be running on this repo.
	if sc.RunningSessionsForDir == nil || sc.RunningSessionsForDir(repo) == 0 {
		return nil, nil
	}

	wt := h.worktreePath(repo, sc.ConversationID)
	if err := gitWorktreeAdd(h.host, repo, sc.ConversationID, wt); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}
	h.repo, h.wt, h.branch = repo, wt, sc.ConversationID

	// Keep the worktree location out of the parent's untracked list (best-effort).
	if err := appendGitExclude(h.host, repo, h.location); err != nil {
		slog.Warn("worktree hook: could not update .git/info/exclude", "repo", repo, "err", err)
	}

	// Redirect the session into the worktree; expose the original repo read-write.
	opts.CWD = wt
	opts.RWBinds = append(opts.RWBinds, repo)

	slog.Info("worktree hook: redirected session to isolated worktree",
		"repo", repo, "worktree", wt, "branch", sc.ConversationID)

	if sc.Notify != nil {
		sc.Notify(fmt.Sprintf("This repo is already in use — working in an isolated worktree: %s", wt))
	}
	return h.teardown, nil
}

// SessionState records the worktree SetupSession created, if any. Implements
// Resumable.
func (h *WorktreeHook) SessionState() map[string]string {
	if h.wt == "" {
		return nil
	}
	return map[string]string{"repo": h.repo, "worktree": h.wt, "branch": h.branch}
}

// ResumeSession reattaches to a worktree created before a server restart, so
// the close guard and teardown keep protecting it. Implements Resumable.
func (h *WorktreeHook) ResumeSession(sc SessionContext, state map[string]string) (func(bool), error) {
	h.host = sc.Host
	if state["worktree"] == "" {
		return nil, nil
	}
	h.repo, h.wt, h.branch = state["repo"], state["worktree"], state["branch"]
	return h.teardown, nil
}

// teardown removes the worktree when the session ends. An unforced teardown
// (crash, shutdown, a close nobody confirmed) must not take uncommitted work
// with it. Leaving the worktree registered keeps it reachable from the origin
// repo — `git worktree list` still shows it — so the changes can be recovered or
// committed later.
func (h *WorktreeHook) teardown(force bool) {
	if !force {
		if reason := h.CanClose(); reason != "" {
			slog.Info("worktree hook: keeping worktree with uncommitted changes",
				"worktree", h.wt, "branch", h.branch, "reason", reason)
			return
		}
	}
	if err := gitWorktreeRemove(h.host, h.repo, h.wt); err != nil {
		slog.Warn("worktree hook: remove failed", "worktree", h.wt, "err", err)
	}
}

// CanClose vetoes a deliberate close while this conversation's worktree holds
// changes that removing it would destroy: modifications to tracked files and
// untracked new files, per `git status --porcelain` (so .gitignore'd files are
// not counted). Work already committed to the worktree's branch is NOT a veto —
// the branch outlives the worktree, so that work is not at risk. Implements
// CloseGuard.
//
// It returns "" — safe to close — whenever it cannot tell: no worktree was
// created for this session, or git could not be run. A veto that cannot be
// cleared would leave a session nobody can stop.
func (h *WorktreeHook) CanClose() string {
	if h.wt == "" {
		return ""
	}
	changes, err := gitStatusPorcelain(h.host, h.wt)
	if err != nil {
		slog.Warn("worktree hook: could not check for uncommitted changes; allowing close",
			"worktree", h.wt, "err", err)
		return ""
	}
	if len(changes) == 0 {
		return ""
	}
	return fmt.Sprintf("the isolated worktree %s has uncommitted changes: %s",
		h.wt, summarize(changes, 5))
}

// gitStatusPorcelain returns the paths git reports as changed in dir: tracked
// modifications plus untracked files, excluding ignored ones.
func gitStatusPorcelain(host process.Host, dir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	out, err := process.Output(ctx, host, "", "git", "-C", dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) <= 3 {
			continue // blank, or too short to carry a path
		}
		// Porcelain v1: "XY path", with renames written "XY old -> new".
		path := strings.TrimSpace(line[3:])
		if _, after, found := strings.Cut(path, " -> "); found {
			path = after
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// summarize joins up to max items, noting how many were left out.
func summarize(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(items[:max], ", "), len(items)-max)
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

// isGitRepo reports whether dir on host contains a .git entry (dir or file).
func isGitRepo(host process.Host, dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	_, err := process.Output(ctx, host, "", "sh", "-c", `test -e "$1"`, "sh", filepath.Join(dir, ".git"))
	return err == nil
}

// gitWorktreeAdd creates a worktree at path on a new branch from current HEAD.
func gitWorktreeAdd(host process.Host, repo, branch, path string) error {
	return runGit(host, repo, "worktree", "add", "-b", branch, path)
}

// gitWorktreeRemove force-removes the worktree at path, keeping its branch.
func gitWorktreeRemove(host process.Host, repo, path string) error {
	return runGit(host, repo, "worktree", "remove", "--force", path)
}

// runGit runs `git -C repo args...` on host, returning its output on error.
func runGit(host process.Host, repo string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitWriteTimeout)
	defer cancel()
	_, err := process.Output(ctx, host, "", append([]string{"git", "-C", repo}, args...)...)
	return err
}

// gitWriteTimeout bounds git commands that change a repo (worktree add and
// remove), which may check out a large tree.
const gitWriteTimeout = 2 * time.Minute

// excludeScript appends $2 to $1/.git/info/exclude unless a line already equals
// it, first terminating a last line that lacks a newline. A shell script (not Go
// file I/O) so it works the same on a remote host.
const excludeScript = `f="$1/.git/info/exclude"
if [ -f "$f" ] && grep -qxF -e "$2" "$f"; then exit 0; fi
mkdir -p "$(dirname "$f")" || exit 1
if [ -s "$f" ] && [ -n "$(tail -c 1 "$f")" ]; then printf '\n' >> "$f"; fi
printf '%s\n' "$2" >> "$f"`

// appendGitExclude adds entry to <repo>/.git/info/exclude if not already present.
func appendGitExclude(host process.Host, repo, entry string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	_, err := process.Output(ctx, host, "", "sh", "-c", excludeScript, "sh", repo, entry)
	return err
}
