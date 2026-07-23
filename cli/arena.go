package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/elek/acpp/arena"
)

// Arena runs several ACP agents on the same task in isolated directories, has
// each agent blindly score every result, and writes a comparative report.
type Arena struct {
	Plan      string `arg:"" help:"Path to the arena plan YAML"`
	OutputDir string `name:"output-dir" short:"o" default:"." help:"Base directory for the run output (run lives in <output-dir>/<plan.name>)"`
}

func (a *Arena) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := arena.Run(ctx, a.Plan, arena.Options{OutputDir: a.OutputDir}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "arena: done")
	return nil
}
