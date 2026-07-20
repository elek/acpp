package cli

import (
	"context"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/permission"
	"github.com/elek/acpp/persistence"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/web"
	"github.com/pkg/errors"
)

// Web starts the web UI. It creates a dedicated router so sessions started from
// the browser run as ACP conversations, streams their updates live over
// WebSockets, and persists them to the store. Runs until interrupted.
type Web struct {
	Addr string `help:"Web server listen address" default:":8080"`
}

func (w *Web) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Derive a cancelable child so the /exit command can bring the app down.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return errors.WithStack(err)
	}

	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	// Any conversation still marked running/pending belongs to a previous process
	// that exited without finalizing it. Mark them complete on startup so stale
	// sessions from earlier runs don't linger as active.
	if n, err := store.CompleteRunningSessions(ctx); err != nil {
		return errors.Wrap(err, "completing stale sessions from previous run")
	} else if n > 0 {
		slog.Info("marked stale sessions from previous run as complete", "count", n)
	}

	rt := router.New(router.WithConfig(cfg))
	defer rt.Close()
	rt.OnShutdown(cancel)

	// The web UI cannot prompt for permissions, so auto-approve them.
	permission.NewAllowAll(rt)

	// Record every conversation's sessions and logs to the store.
	persistence.New(rt, store)

	addr := resolveWebAddr(w.Addr, cfg)
	srv := web.New(store, addr).
		WithProjects(store).
		WithRouter(rt).
		WithDefaults(web.SessionDefaults{
			Agent:   cfg.ResolveAgent(cfg.Defaults.Agent),
			Sandbox: cfg.Defaults.Sandbox,
		})

	slog.Info("starting web server", "addr", addr)
	return srv.Start(ctx)
}

// resolveWebAddr prefers the explicit --addr flag, falling back to web_addr in
// config when the flag is left at its default.
func resolveWebAddr(flagAddr string, cfg *config.Config) string {
	if flagAddr != "" && flagAddr != ":8080" {
		return flagAddr
	}
	if cfg.WebAddr != "" {
		return cfg.WebAddr
	}
	return ":8080"
}
