package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/sandbox"
)

// Sandbox starts an interactive command (bash by default) inside a sandbox,
// forwarding stdin/stdout/stderr. Sandbox type and profiles are resolved from
// the project's stored config and global config, and can be overridden with
// flags — so the sandbox here matches the one the project's sessions get.
type Sandbox struct {
	SandboxType string   `name:"sandbox" help:"Sandbox type override (bbwrap, none)"`
	Profiles    string   `help:"Comma-separated profiles; replaces the project's stored profiles when set"`
	Command     []string `arg:"" optional:"" passthrough:"" help:"Command to run inside the sandbox (default: /usr/bin/bash)"`
}

// Run resolves the sandbox settings, wraps the command, and execs it with the
// current process's stdio inherited so the caller gets an interactive session.
func (s *Sandbox) Run(kctx *kong.Context) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	project := projectSandboxConfig(context.Background(), cfg, cwd)
	sbType, profiles := resolveSandboxSettings(s.SandboxType, s.Profiles, project, cfg.Defaults.Sandbox)

	sb, err := sandbox.ResolveSandbox(sbType, profiles, cwd, nil, nil)
	if err != nil {
		return fmt.Errorf("resolving sandbox %q: %w", sbType, err)
	}

	command := s.Command
	if len(command) == 0 {
		command = []string{"/usr/bin/bash"}
	}

	name, args := sb.Wrap(command[0], command[1:])

	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		// Propagate the child's exit code so scripts can rely on it.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("running sandboxed command: %w", err)
	}
	return nil
}

// projectSandboxConfig reads the sandbox settings stored for the project owning
// cwd, named (as everywhere else) after the directory's base. It is best-effort:
// a command that only wants to open a shell should not fail because the database
// is unreachable, so a connection error degrades to "no project config" with a
// warning rather than aborting.
//
// It deliberately does not go through openStore, whose CompleteRunningSessions
// call would mark live sessions from a running server as complete.
func projectSandboxConfig(ctx context.Context, cfg *config.Config, cwd string) db.ProjectRow {
	if cfg.Database.DSN == "" {
		return db.ProjectRow{}
	}
	store, err := db.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		slog.Warn("could not read project config; using flags and global config only", "error", err)
		return db.ProjectRow{}
	}
	defer store.Close()

	p, err := store.GetProject(ctx, filepath.Base(cwd))
	if err != nil {
		slog.Warn("could not read project config; using flags and global config only", "error", err)
		return db.ProjectRow{}
	}
	return p
}

// resolveSandboxSettings picks the sandbox type and profiles following the
// precedence: CLI flag > project's stored config > global config default. The
// type falls back to "bbwrap" when nothing is set; CLI profiles fully replace
// the project's profiles rather than merging.
func resolveSandboxSettings(flagType, flagProfiles string, project db.ProjectRow, defaultSandbox string) (sbType, profiles string) {
	sbType = flagType
	if sbType == "" {
		sbType = project.Sandbox
	}
	if sbType == "" {
		sbType = defaultSandbox
	}
	if sbType == "" {
		sbType = "bbwrap"
	}

	profiles = flagProfiles
	if profiles == "" {
		profiles = project.SandboxProfiles
	}
	return sbType, profiles
}
