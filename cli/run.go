package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/permission"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
)

type Run struct {
	Agent  string   `arg:"" help:"Agent command to run (e.g., claude-code-acp)"`
	Prompt []string `arg:"" optional:"" help:"Prompt to send (if not provided, reads from stdin)"`
}

// Run executes a single full agent lifecycle: it spawns the agent through the
// router, sends one prompt, streams the response to stdout, then shuts the
// agent down and exits.
func (r *Run) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	prompt, err := r.resolvePrompt()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	rt := router.New()
	defer rt.Close()

	// Auto-approve every permission request the agent issues.
	permission.NewAllowAll(rt)

	// Stream the agent's text output to stdout as it arrives.
	p := &printer{w: os.Stdout, done: make(chan struct{})}
	rt.Subscribe(p.Receive)

	id, err := rt.Create(ctx, types.SessionOpts{
		ProjectID: cwd,
		Agent:     r.Agent,
		CWD:       cwd,
		Source:    "cli",
	})
	if err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}

	meta, err := rt.WaitReady(ctx, id)
	if err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	if err := rt.Send(ctx, meta, acp.PromptRequest{
		SessionId: meta.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
	}); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

	// Send no longer blocks for the turn; wait for the PromptResponse (signalled
	// by the printer) before shutting the agent down.
	select {
	case <-p.done:
	case <-ctx.Done():
	}

	fmt.Fprintln(os.Stderr)
	return nil
}

// resolvePrompt joins the positional prompt args, falling back to stdin.
func (r *Run) resolvePrompt() (string, error) {
	prompt := strings.TrimSpace(strings.Join(r.Prompt, " "))
	if prompt != "" {
		return prompt, nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read prompt from stdin: %w", err)
	}
	prompt = strings.TrimSpace(string(data))
	if prompt == "" {
		return "", fmt.Errorf("no prompt provided (pass as arguments or via stdin)")
	}
	return prompt, nil
}

// printer is a router.Subscriber that writes agent message text to w and signals
// done when the prompt turn completes.
type printer struct {
	w    io.Writer
	done chan struct{}
}

func (p *printer) Receive(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	switch m := msg.(type) {
	case acp.SessionNotification:
		if c := m.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
			fmt.Fprint(p.w, c.Content.Text.Text)
		}
	case acp.PromptResponse:
		fmt.Fprintf(os.Stderr, "\n[stop reason: %s]", m.StopReason)
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}
}
