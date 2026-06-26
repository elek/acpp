package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/permission"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
)

type Cat struct {
	Agent         string `arg:"" help:"Agent command to run (e.g., claude-code-acp)"`
	PrintResponse bool   `help:"Also print the PromptResponse as JSONL after each prompt" short:"r"`
}

// Run reads prompts from stdin line by line, sends each to the agent, and
// streams every session update to stdout as JSONL. It is a low-level inspection
// tool for the raw ACP event stream.
func (a *Cat) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	rt := router.New()
	defer rt.Close()

	// Auto-approve every permission request the agent issues.
	permission.NewAllowAll(rt)

	p := &catPrinter{
		w:             os.Stdout,
		printResponse: a.PrintResponse,
		turns:         make(chan acp.PromptResponse, 1),
	}
	rt.Subscribe(p.Receive)

	id, err := rt.Create(ctx, types.SessionOpts{
		ProjectID: cwd,
		Agent:     a.Agent,
		CWD:       cwd,
		Source:    "acpcat",
	})
	if err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}

	meta, err := rt.WaitReady(ctx, id)
	if err != nil {
		return fmt.Errorf("wait ready: %w", err)
	}

	// Read prompts from stdin line by line.
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if err := rt.Send(ctx, meta, acp.PromptRequest{
			SessionId: meta.SessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock(line)},
		}); err != nil {
			return fmt.Errorf("prompt: %w", err)
		}

		// Send no longer blocks for the turn; wait for the PromptResponse
		// before sending the next prompt.
		select {
		case <-p.turns:
		case <-ctx.Done():
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	if summary := p.usageSummary(); summary != "" {
		fmt.Fprintln(os.Stderr, summary)
	}
	return nil
}

// catPrinter is a router.Subscriber that writes every session update to w as
// JSONL and signals turn completion on turns.
type catPrinter struct {
	w             io.Writer
	printResponse bool
	turns         chan acp.PromptResponse

	inputTokens  int
	outputTokens int
	totalTokens  int
}

func (p *catPrinter) Receive(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	switch m := msg.(type) {
	case acp.SessionNotification:
		if data, err := json.Marshal(m.Update); err == nil {
			fmt.Fprintln(p.w, string(data))
		}
	case acp.PromptResponse:
		if p.printResponse {
			if data, err := json.Marshal(m); err == nil {
				fmt.Fprintln(p.w, string(data))
			}
		}
		if m.Usage != nil {
			p.inputTokens += m.Usage.InputTokens
			p.outputTokens += m.Usage.OutputTokens
			p.totalTokens += m.Usage.TotalTokens
		}
		select {
		case p.turns <- m:
		default:
		}
	}
}

// usageSummary formats the accumulated token usage, or "" if none was reported.
func (p *catPrinter) usageSummary() string {
	if p.totalTokens == 0 && p.inputTokens == 0 && p.outputTokens == 0 {
		return ""
	}
	return fmt.Sprintf("usage: input=%d output=%d total=%d", p.inputTokens, p.outputTokens, p.totalTokens)
}
