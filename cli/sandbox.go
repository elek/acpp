package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/alecthomas/kong"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/sandbox"
)

// Sandbox starts an interactive command (bash by default) inside a sandbox,
// forwarding stdin/stdout/stderr. Sandbox type and profiles are resolved from
// the project's .acpp.yaml and global config, and can be overridden with flags.
type Sandbox struct {
	SandboxType string   `name:"sandbox" help:"Sandbox type override (bbwrap, none)"`
	Profiles    string   `help:"Comma-separated profiles; replaces .acpp.yaml profiles when set"`
	Command     []string `arg:"" optional:"" passthrough:"" help:"Command to run inside the sandbox (default: /usr/bin/bash)"`
}

// Run resolves the sandbox settings, wraps the command, and execs it with the
// current process's stdio inherited so the caller gets an interactive session.
func (s *Sandbox) Run(kctx *kong.Context) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	pc, err := config.LoadProject(cwd)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	sbType, profiles := resolveSandboxSettings(s.SandboxType, s.Profiles, pc, cfg.Defaults.Sandbox)

	sb, err := sandbox.ResolveSandbox(sbType, profiles, cwd)
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

// resolveSandboxSettings picks the sandbox type and profiles following the
// precedence: CLI flag > project .acpp.yaml > global config default. The type
// falls back to "bbwrap" when nothing is set; CLI profiles fully replace the
// project's profiles rather than merging.
func resolveSandboxSettings(flagType, flagProfiles string, pc config.ProjectConfig, defaultSandbox string) (sbType, profiles string) {
	sbType = flagType
	if sbType == "" {
		sbType = pc.Sandbox.Name
	}
	if sbType == "" {
		sbType = defaultSandbox
	}
	if sbType == "" {
		sbType = "bbwrap"
	}

	profiles = flagProfiles
	if profiles == "" {
		profiles = pc.Sandbox.Profiles
	}
	return sbType, profiles
}
