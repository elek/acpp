package cli

import (
	"context"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/web"
	"github.com/pkg/errors"
)

type Web struct {
	Addr string `help:"Web server listen address" default:":8080"`
}

func (w *Web) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return errors.WithStack(err)
	}

	store, err := db.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		return errors.Wrap(err, "database connection failed")
	}
	defer store.Close()

	slog.Info("starting web server", "addr", w.Addr)
	return web.New(store, w.Addr).WithProjects(store).Start(ctx)
}
