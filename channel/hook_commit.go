package channel

import (
	"log/slog"
	"os/exec"
	"strings"
)

func init() {
	RegisterHook("commit", func() Hook { return NewCommitHook() })
}

// CommitHook automatically commits dirty git working trees after prompts.
// Each session gets its own instance so that hasCommitted state is independent.
type CommitHook struct {
	hasCommitted bool
}

// NewCommitHook creates a new CommitHook.
func NewCommitHook() *CommitHook {
	return &CommitHook{}
}

func (h *CommitHook) OnSessionStarted(cwd string) {}

func (h *CommitHook) BeforeFirstPrompt(cwd string, prompt string) string {
	return prompt
}

func (h *CommitHook) AfterPromptFinished(cwd string, prompt string, promptFunc PromptFunc) {
	// Skip short prompts — likely answers to questions, confirmations, or numbers.
	if len(prompt) <= 100 {
		return
	}

	if !isGitDirty(cwd) {
		return
	}

	commitPrompt := "commit"
	if h.hasCommitted {
		commitPrompt = "commit amend"
	}

	slog.Info("commit hook: sending follow-up prompt",
		"cwd", cwd, "prompt", commitPrompt, "hasCommitted", h.hasCommitted)

	if err := promptFunc(commitPrompt); err != nil {
		slog.Error("commit hook: follow-up prompt failed", "err", err)
		return
	}

	h.hasCommitted = true
}

// isGitDirty returns true if dir is inside a git repo with uncommitted changes.
func isGitDirty(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return false
	}

	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}
