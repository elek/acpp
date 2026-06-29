package cli

import (
	"context"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/discord"
	"github.com/elek/acpp/permission"
	"github.com/elek/acpp/persistence"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/web"
	"github.com/pkg/errors"
)

// Serve starts the web UI and the Discord bot together on a single shared
// router, so conversations created from either surface stream through the same
// event hub and persist to the same store. Runs until interrupted (SIGINT/
// SIGTERM) or the /exit command.
type Serve struct {
	Addr       string   `help:"Web server listen address" default:":8080"`
	Token      string   `help:"Discord bot token (defaults to discord_token in config)"`
	Agent      string   `help:"Agent command to run for conversations (defaults to config)"`
	SearchPath []string `help:"Directories searched for a project matching the channel name (defaults to search_path in config)"`
}

func (s *Serve) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Derive a cancelable child so the /exit command (and a web start failure)
	// can bring the whole app down the same way a signal does.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return errors.WithStack(err)
	}

	token := s.Token
	if token == "" {
		token = cfg.DiscordToken
	}
	if token == "" {
		return errors.New("no Discord token provided (pass --token or set discord_token in config)")
	}

	agent := s.Agent
	if agent == "" {
		agent = cfg.Defaults.Agent
	}
	agent = cfg.ResolveAgent(agent)

	searchPaths := s.SearchPath
	if len(searchPaths) == 0 {
		searchPaths = cfg.SearchPath
	}

	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	// One router, shared by both surfaces.
	rt := router.New(router.WithConfig(cfg))
	defer rt.Close()
	rt.OnShutdown(cancel)

	// Auto-approve permission requests (neither surface prompts for them). Must be
	// registered exactly once on the shared router.
	permission.NewAllowAll(rt)
	rt.Subscribe(router.Debug)

	// Record every conversation's sessions and logs to the store (shared by both
	// surfaces).
	persistence.New(rt, store)

	// Discord: each channel maps to a conversation on the shared router.
	dc, err := discord.NewDiscordChannel(token, agent, searchPaths, rt)
	if err != nil {
		return errors.Wrap(err, "starting Discord integration")
	}
	defer dc.Close()

	// Web: streams every conversation (including Discord's) and can start its own.
	addr := resolveWebAddr(s.Addr, cfg)
	srv := web.New(store, addr).
		WithProjects(store).
		WithRouter(rt).
		WithDefaults(web.SessionDefaults{
			Agent:   agent,
			Sandbox: cfg.Defaults.Sandbox,
		})

	go func() {
		slog.Info("starting web server", "addr", addr)
		if err := srv.Start(ctx); err != nil {
			slog.Error("web server stopped", "error", err)
			cancel()
		}
	}()

	// Scheduler: cron-triggered prompts run as conversations on the shared router.
	// It subscribes for completion (PromptResponse) events to close finished runs.
	sched := router.NewScheduler(rt, cfg, store)
	rt.Subscribe(sched.Receive)
	if err := sched.Start(ctx); err != nil {
		return errors.Wrap(err, "starting scheduler")
	}

	<-ctx.Done()
	return nil
}
