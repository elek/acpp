package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/elek/acpp/sandbox"
)

// LocalName is the location of the machine acpp itself runs on. A project whose
// location is empty or LocalName runs its agent locally.
const LocalName = "localhost"

// IsLocal reports whether location names this machine.
func IsLocal(location string) bool {
	return location == "" || location == LocalName
}

// Host is a machine that can run agents and short commands: this one (Local) or
// a remote agent connected over the network. Everything that touches the disk a
// session works on goes through its Host, so a session on a remote machine sees
// that machine's files.
type Host interface {
	// Name is the host's location name.
	Name() string
	// Start launches an agent subprocess.
	Start(ctx context.Context, spec Spec) (Handle, error)
	// Exec runs a command to completion. A non-zero exit is reported in
	// ExecResult.ExitCode, not as an error; the error is for failing to run it
	// at all (missing binary, host unreachable, ctx done).
	Exec(ctx context.Context, spec ExecSpec) (ExecResult, error)
}

// ExecSpec describes a short command to run on a host.
type ExecSpec struct {
	Argv []string `json:"argv"`
	Dir  string   `json:"dir,omitempty"`
	// Env entries are appended to the host's environment.
	Env []string `json:"env,omitempty"`
	// Stdin is fed to the command.
	Stdin []byte `json:"stdin,omitempty"`
	// Sandbox, when set, wraps the command (local host only: a built sandbox
	// references this machine's files).
	Sandbox sandbox.Sandbox `json:"-"`
	// SessionSandbox names a conversation whose agent sandbox the command should
	// run in. The remote host uses it; the local host relies on Sandbox.
	SessionSandbox string `json:"session_sandbox,omitempty"`
	// Combined merges stderr into Stdout, in order.
	Combined bool `json:"combined,omitempty"`
	// TimeoutMs bounds the command on a remote host, which cannot see the
	// caller's context (the local host uses ctx directly).
	TimeoutMs int64 `json:"timeout_ms,omitempty"`
}

// ExecResult is the outcome of a command that ran.
type ExecResult struct {
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// OK reports a zero exit.
func (r ExecResult) OK() bool { return r.ExitCode == 0 }

// Err converts a non-zero exit to an error carrying the command's output, or
// returns nil.
func (r ExecResult) Err(argv []string) error {
	if r.OK() {
		return nil
	}
	out := strings.TrimSpace(string(r.Stderr))
	if out == "" {
		out = strings.TrimSpace(string(r.Stdout))
	}
	return fmt.Errorf("%s: exit status %d: %s", strings.Join(argv, " "), r.ExitCode, out)
}

// Output runs argv in dir on host and returns its stdout, failing on a non-zero
// exit. A nil host means this machine.
func Output(ctx context.Context, host Host, dir string, argv ...string) ([]byte, error) {
	if host == nil {
		host = Local
	}
	res, err := host.Exec(ctx, ExecSpec{Argv: argv, Dir: dir})
	if err != nil {
		return nil, err
	}
	if err := res.Err(argv); err != nil {
		return res.Stdout, err
	}
	return res.Stdout, nil
}

// LocalHost runs agents and commands on this machine.
type LocalHost struct {
	procs *Manager
}

// Local is the host for this machine, backed by DefaultManager.
var Local = NewLocalHost(DefaultManager)

// NewLocalHost returns a Host that spawns through m.
func NewLocalHost(m *Manager) *LocalHost { return &LocalHost{procs: m} }

// Name implements Host.
func (h *LocalHost) Name() string { return LocalName }

// Manager returns the process manager the host spawns through.
func (h *LocalHost) Manager() *Manager { return h.procs }

// Start implements Host.
func (h *LocalHost) Start(ctx context.Context, spec Spec) (Handle, error) {
	p, err := h.procs.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Exec implements Host.
func (h *LocalHost) Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	return RunLocal(ctx, spec)
}

// RunLocal runs spec on this machine.
func RunLocal(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	if len(spec.Argv) == 0 {
		return ExecResult{}, errors.New("exec: empty command")
	}
	name, args := spec.Argv[0], spec.Argv[1:]
	if spec.Sandbox != nil {
		name, args = spec.Sandbox.Wrap(name, args)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	// Without a WaitDelay, killing the command on ctx cancel is not enough: any
	// grandchild it left behind still holds the output pipe open.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if spec.Combined {
		cmd.Stderr = &stdout
	} else {
		cmd.Stderr = &stderr
	}
	err := cmd.Run()
	res := ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		res.ExitCode = exitErr.ExitCode()
	default:
		// Not started, or killed by a signal (ctx timeout): not an exit status.
		if ctx.Err() != nil {
			return res, fmt.Errorf("exec %s: %w", strings.Join(spec.Argv, " "), ctx.Err())
		}
		return res, fmt.Errorf("exec %s: %w", strings.Join(spec.Argv, " "), err)
	}
	return res, nil
}
