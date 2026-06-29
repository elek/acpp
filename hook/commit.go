package hook

import (
	"log/slog"
	"os/exec"
	"strings"

	"github.com/elek/acpp/acp"
)

func init() {
	Register("commit", func(params map[string]string) (Hook, error) {
		return NewCommitHook(), nil
	})
}

// CommitHook auto-commits a dirty git working tree after each substantive prompt
// turn by injecting a follow-up "commit" prompt. Each conversation gets its own
// instance so hasCommitted state is independent.
type CommitHook struct {
	hasCommitted bool
	lastPrompt   string
	// isDirty reports whether dir is a git repo with uncommitted changes. A field
	// so tests can substitute it.
	isDirty func(dir string) bool
}

// NewCommitHook creates a CommitHook backed by the real git working-tree check.
func NewCommitHook() *CommitHook {
	return &CommitHook{isDirty: isGitDirty}
}

// Outgoing records the latest user prompt text so Incoming can decide whether the
// turn was substantive enough to warrant a commit.
func (h *CommitHook) Outgoing(hc HookContext, msg any) any {
	if req, ok := msg.(acp.PromptRequest); ok {
		h.lastPrompt = promptText(req)
	}
	return msg
}

// Incoming watches for the PromptResponse that ends a turn. When the turn was
// substantive (a long prompt, not a quick confirmation) and the tree is dirty, it
// triggers a follow-up "commit" prompt. The original message is always delivered.
func (h *CommitHook) Incoming(hc HookContext, msg any) any {
	if _, ok := msg.(acp.PromptResponse); !ok {
		return msg
	}

	// Skip short prompts — likely answers to questions, confirmations, or numbers.
	if len(h.lastPrompt) <= 100 {
		return msg
	}
	if h.isDirty == nil || !h.isDirty(hc.CWD) {
		return msg
	}

	commitPrompt := "commit"
	if h.hasCommitted {
		commitPrompt = "commit amend"
	}

	slog.Info("commit hook: triggering follow-up prompt",
		"cwd", hc.CWD, "prompt", commitPrompt, "hasCommitted", h.hasCommitted)

	if err := hc.Trigger(commitPrompt); err != nil {
		slog.Error("commit hook: follow-up prompt failed", "err", err)
		return msg
	}
	h.hasCommitted = true
	return msg
}

// promptText concatenates the text blocks of a prompt request.
func promptText(req acp.PromptRequest) string {
	var b strings.Builder
	for _, block := range req.Prompt {
		if block.Text != nil {
			b.WriteString(block.Text.Text)
		}
	}
	return b.String()
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
