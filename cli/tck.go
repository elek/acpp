package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/tck"
)

// Tck tests one or more ACP agent binaries by running a fixed scenario of real
// prompts and reporting a matrix of compatibility properties.
type Tck struct {
	Agents  []string      `arg:"" help:"One or more ACP agent commands to test (e.g., claude-code-acp)"`
	JSON    bool          `help:"Emit JSON instead of a table"`
	Timeout time.Duration `help:"Per-prompt timeout" default:"120s"`
}

func (t *Tck) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	reports := make([]tck.AgentReport, 0, len(t.Agents))
	for _, agent := range t.Agents {
		if !t.JSON {
			fmt.Fprintf(os.Stderr, "testing %s ...\n", agent)
		}
		runner := &tck.Runner{Agent: agent, Timeout: t.Timeout}
		results, err := runner.Run(ctx)
		rep := tck.AgentReport{Agent: agent}
		if err != nil {
			rep.Error = err.Error()
			rep.Results = []tck.Result{{Name: "startup", OK: false, Value: err.Error()}}
		} else {
			rep.Results = append([]tck.Result{{Name: "startup", OK: true, Value: "ok"}}, results...)
		}
		reports = append(reports, rep)

		if ctx.Err() != nil {
			break
		}
	}

	if t.JSON {
		return tck.RenderJSON(os.Stdout, reports)
	}
	tck.RenderTable(os.Stdout, reports)
	return nil
}
