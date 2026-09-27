package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/remote"
)

// Remote runs this machine as a remote agent location: it connects to an
// `acpp serve` and runs the agents of the projects whose location names it.
// Agent commands and sandbox profiles are resolved against this machine's own
// config (agent_path, sandbox profiles).
type Remote struct {
	Server     string `help:"Server URL, e.g. https://acpp.example.com (defaults to remote.server in config)"`
	SecretFile string `help:"File holding the shared secret (defaults to remote.secret / remote.secret_file in config)" type:"path"`
	Location   string `help:"Location name to register under (defaults to remote.location in config, then the hostname)"`
}

func (r *Remote) Run(kctx *kong.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	rc := cfg.Remote
	if r.Server != "" {
		rc.Server = r.Server
	}
	if r.SecretFile != "" {
		rc.Secret, rc.SecretFile = "", r.SecretFile
	}
	if r.Location != "" {
		rc.Location = r.Location
	}
	if rc.Server == "" {
		return errors.New("no server configured: pass --server or set remote.server in config")
	}
	secret, err := rc.ResolveSecret()
	if err != nil {
		return err
	}
	if secret == "" {
		return errors.New("no secret configured: pass --secret-file or set remote.secret in config")
	}
	location := rc.Location
	if location == "" {
		location, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("no location configured and the hostname is unavailable: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("starting remote agent", "server", rc.Server, "location", location)
	agent := &remote.Agent{
		Server:       rc.Server,
		Secret:       secret,
		Location:     location,
		ResolveAgent: cfg.ResolveAgent,
	}
	return agent.Run(ctx)
}
