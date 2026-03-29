package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/coder/acp-go-sdk"
	acp2 "github.com/elek/acpp/acp"
	"github.com/elek/acpp/channel"
	"github.com/elek/acpp/console"
	"github.com/google/uuid"
)

type Run struct {
	Agent  string   `arg:"" help:"Agent command to run (e.g., claude-code-acp)"`
	Prompt []string `arg:"" optional:"" help:"Prompt to send (if not provided, reads from stdin)"`
}

func (r *Run) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cwd, _ := os.Getwd()
	sess := acp2.NewSession(uuid.NewString(), acp2.SessionOpts{
		Source: "run",
		Agent:  r.Agent,
		CWD:    cwd,
	})

	if err := sess.Start(); err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	defer sess.Close()

	// Set up console channel for formatted output
	ch, err := console.CreateChannel(ctx)
	if err != nil {
		return fmt.Errorf("create console channel: %w", err)
	}
	source := channel.SourceID("console")
	broadcast := func(fn func(channel.Channel, channel.SourceID)) {
		fn(ch, source)
	}
	relay := channel.NewRelay(sess, broadcast, nil)

	<-relay.Ready()
	if sess.GetStatus() == acp2.StatusError {
		return fmt.Errorf("session error: %s", sess.GetError())
	}

	// Determine prompt source: args or stdin
	if len(r.Prompt) > 0 {
		prompt := strings.Join(r.Prompt, " ")
		_, err := sess.Prompt([]acp.ContentBlock{acp.TextBlock(prompt)})
		if err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
		relay.WaitForPending()
	} else {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			_, err := sess.Prompt([]acp.ContentBlock{acp.TextBlock(line)})
			if err != nil {
				return fmt.Errorf("prompt: %w", err)
			}
			relay.WaitForPending()
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
	}

	ch.FinishConversation(source, acp.PromptResponse{})

	// Print usage summary to stderr
	info := sess.GetStatusInfo()
	if summary := info.FormatUsageSummary(); summary != "" {
		fmt.Fprintln(os.Stderr, summary)
	}

	return nil
}
