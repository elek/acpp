package cli

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/discord"
	"github.com/elek/acpp/permission"
	"github.com/elek/acpp/router"
	"github.com/pkg/errors"
)

// Discord starts only the Discord integration: it creates a dedicated router and
// wires a Discord bot to it, so each Discord channel maps to an ACP conversation.
// The command runs until interrupted (SIGINT/SIGTERM).
type Discord struct {
	Token      string   `help:"Discord bot token (defaults to discord_token in config)"`
	Agent      string   `help:"Agent command to run for conversations (defaults to config)"`
	SearchPath []string `help:"Directories searched for a project matching the channel name (defaults to search_path in config)"`
}

func (d *Discord) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Derive a cancelable child so the /exit command can bring the app down the
	// same way a signal does (unblocking <-ctx.Done() below).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return errors.WithStack(err)
	}

	token := d.Token
	if token == "" {
		token = cfg.DiscordToken
	}
	if token == "" {
		return errors.New("no Discord token provided (pass --token or set discord_token in config)")
	}

	agent := d.Agent
	if agent == "" {
		agent = cfg.Defaults.Agent
	}
	agent = cfg.ResolveAgent(agent)

	searchPaths := d.SearchPath
	if len(searchPaths) == 0 {
		searchPaths = cfg.SearchPath
	}

	rt := router.New(router.WithConfig(cfg))
	defer rt.Close()
	rt.OnShutdown(cancel)

	// Auto-approve every permission request the agent issues.
	permission.NewAllowAll(rt)
	rt.Subscribe(router.Debug)

	dc, err := discord.NewDiscordChannel(token, agent, searchPaths, rt)
	if err != nil {
		return errors.Wrap(err, "starting Discord integration")
	}
	defer dc.Close()

	// NewDiscordChannel opens the session and registers event handlers; block
	// until interrupted, then the deferred closes tear everything down.
	<-ctx.Done()
	return nil
}
