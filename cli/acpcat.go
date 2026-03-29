package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	"github.com/coder/acp-go-sdk"
	acp2 "github.com/elek/acpp/acp"
	"github.com/google/uuid"
)

type Cat struct {
	Agent         string `arg:"" help:"Agent command to run (e.g., claude-code-acp)"`
	PrintResponse bool   `help:"Also print the PromptResponse as JSONL after each prompt" short:"r"`
}

func (a *Cat) Run(kctx *kong.Context) error {
	cwd, _ := os.Getwd()
	sess := acp2.NewSession(uuid.NewString(), acp2.SessionOpts{
		Source: "acpcat",
		Agent:  a.Agent,
		CWD:    cwd,
	})

	if err := sess.Start(); err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	defer sess.Close()

	// Wait for session to be ready (first event is the readiness signal)
	updates := sess.Ready()
	<-updates

	// Print subsequent updates as JSONL in the background
	go func() {
		for event := range updates {
			data, err := json.Marshal(event.Update)
			if err != nil {
				continue
			}
			fmt.Println(string(data))
		}
	}()

	if sess.GetStatus() == acp2.StatusError {
		return fmt.Errorf("session error: %s", sess.GetError())
	}

	// Read prompts from stdin line by line
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		resp, err := sess.Prompt([]acp.ContentBlock{acp.TextBlock(line)})
		if err != nil {
			return fmt.Errorf("prompt: %w", err)
		}

		if a.PrintResponse {
			data, err := json.Marshal(resp)
			if err != nil {
				return fmt.Errorf("marshal response: %w", err)
			}
			fmt.Println(string(data))
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	// Print usage summary to stderr on exit
	info := sess.GetStatusInfo()
	if summary := info.FormatUsageSummary(); summary != "" {
		fmt.Fprintln(os.Stderr, summary)
	}

	return nil
}
