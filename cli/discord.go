package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/channel"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/discord"
	"github.com/elek/acpp/metrics"
	"github.com/elek/acpp/web"
	"github.com/pkg/errors"
)

type Discord struct {
	DiscordToken string `env:"DISCORD_TOKEN" help:"Discord bot token (optional)"`
	MetricsAddr  string `help:"Prometheus metrics listen address" default:":9090"`
}

// Run starts the HTTP server and optionally the Discord bot
func (s *Discord) Run(kctx *kong.Context) error {
	// Create signal context at top level - shared by all services
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

	token := s.DiscordToken
	if token == "" {
		token = cfg.DiscordToken
	}

	sessions := acp.NewSessionManager()
	channels := channel.NewChannelManager()
	channels.Register("discord", discord.NewDiscordChannel(token))
	defer channels.Close()

	gw := channel.NewGateway(cfg, store, store, sessions, channels)
	if err := gw.Start(ctx); err != nil {
		return errors.WithStack(err)
	}

	cleanup, err := metrics.Start(ctx, s.MetricsAddr, cfg.OTLP, gw.GetAllStatusInfo)
	if err != nil {
		return errors.WithStack(err)
	}
	defer cleanup()

	if cfg.WebAddr != "" {
		cwd, _ := os.Getwd()
		srv := web.New(store, cfg.WebAddr).
			WithCloser(gw).
			WithPrompter(gw).
			WithCreator(gw).
			WithCommands(gw).
			WithProjects(store).
			WithDefaults(web.SessionDefaults{
				Agent:   cfg.Defaults.Agent,
				Dir:     cwd,
				Sandbox: cfg.Defaults.Sandbox,
			})
		gw.WithPublisher(srv)
		go srv.Start(ctx)
	}

	<-ctx.Done()
	return nil
}
