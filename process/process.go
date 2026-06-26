// Package process owns the OS-level lifecycle of agent subprocesses. It knows
// nothing about the ACP protocol: it spawns a command (optionally wrapped in a
// sandbox), exposes its stdin/stdout pipes and PID, and handles graceful
// shutdown. Everything above it (the ACP connection) works against the returned
// Process.
package process

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/elek/acpp/sandbox"
	"github.com/pkg/errors"
)

// Spec describes an agent subprocess to launch.
type Spec struct {
	Agent   string          // full command string, e.g. "claude-code-acp --flag"
	Cwd     string          // working directory
	Env     []string        // KEY=VALUE entries appended to os.Environ()
	Sandbox sandbox.Sandbox // optional sandbox wrapper; nil means run directly
}

// Process is a running agent subprocess. It owns the OS-level concerns —
// pipes, PID, and graceful shutdown — and is the only handle the ACP layer
// needs to talk to the agent.
type Process struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser

	cmd    *exec.Cmd
	cancel context.CancelFunc
	pid    int
	done   chan struct{}
	stderr *bytes.Buffer

	mu          sync.Mutex
	stdinClosed bool
}

// PID returns the subprocess PID.
func (p *Process) PID() int { return p.pid }

// Done is closed once the subprocess has exited and been reaped.
func (p *Process) Done() <-chan struct{} { return p.done }

// Stderr returns the captured stderr output so far (trimmed).
func (p *Process) Stderr() string { return strings.TrimSpace(p.stderr.String()) }

// Close shuts the subprocess down gracefully: it closes stdin so a well-behaved
// agent sees EOF and exits, waits up to 5s, then falls back to SIGTERM of the
// whole process group via context cancellation (cmd.WaitDelay escalates to
// SIGKILL after another 5s).
func (p *Process) Close() {
	p.mu.Lock()
	if !p.stdinClosed && p.Stdin != nil {
		p.Stdin.Close()
		p.stdinClosed = true
	}
	p.mu.Unlock()

	select {
	case <-p.done:
		return // exited on its own — no signal needed
	case <-time.After(5 * time.Second):
	}

	p.cancel() // SIGTERM -> WaitDelay -> SIGKILL
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
	}
}

// Manager spawns and tracks agent subprocesses. It is the dedicated owner of
// OS processes; nothing else in the system spawns agents directly.
type Manager struct {
	mu   sync.Mutex
	live map[int]*Process
}

// NewManager creates an empty Manager.
func NewManager() *Manager {
	return &Manager{live: make(map[int]*Process)}
}

// DefaultManager is used when no explicit Manager is injected. The router will
// own an explicit Manager once it takes over conversation lifecycle.
var DefaultManager = NewManager()

// Start launches the agent subprocess described by spec, wrapped in a sandbox
// when one is provided, and returns a Process exposing its pipes and lifecycle.
// The process is bound to a child of parent, so cancelling parent also
// terminates the subprocess.
func (m *Manager) Start(parent context.Context, spec Spec) (*Process, error) {
	ctx, cancel := context.WithCancel(parent)

	agentArgs := strings.Fields(spec.Agent)
	cmdName, args := agentArgs[0], agentArgs[1:]
	if spec.Sandbox != nil {
		cmdName, args = spec.Sandbox.Wrap(agentArgs[0], agentArgs[1:])
	}
	slog.Info("spawning agent", "cmd", strings.Join(append([]string{cmdName}, args...), " "))

	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = spec.Cwd // so sandbox.sh binds the correct path
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Send SIGTERM to the entire process group so children (e.g. inside the
		// bwrap sandbox) are also terminated.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, errors.WithStack(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, errors.WithStack(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, errors.WithStack(err)
	}

	// Capture stderr for logging (e.g. sandbox errors).
	var stderrBuf bytes.Buffer
	go io.Copy(&stderrBuf, stderr)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, errors.WithStack(err)
	}

	p := &Process{
		Stdin:  stdin,
		Stdout: stdout,
		cmd:    cmd,
		cancel: cancel,
		pid:    cmd.Process.Pid,
		done:   make(chan struct{}),
		stderr: &stderrBuf,
	}

	m.mu.Lock()
	m.live[p.pid] = p
	m.mu.Unlock()

	logger := slog.Default().With("pid", p.pid)
	// Run cmd.Wait() immediately so Go's exec.CommandContext can enforce the
	// SIGTERM -> WaitDelay -> SIGKILL escalation when the context is cancelled.
	go func() {
		defer close(p.done)
		waitErr := cmd.Wait()
		m.mu.Lock()
		delete(m.live, p.pid)
		m.mu.Unlock()
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if waitErr != nil {
			attrs := []any{"error", waitErr}
			if stderrStr != "" {
				attrs = append(attrs, "stderr", stderrStr)
			}
			logger.Warn("agent subprocess exited with error", attrs...)
		} else {
			logger.Info("agent subprocess exited normally")
		}
	}()

	return p, nil
}

// ClosePID gracefully shuts down the single live process with the given PID, if
// it is still tracked. Intended for closing one conversation while others keep
// running. Returns true if a process was found and closed.
func (m *Manager) ClosePID(pid int) bool {
	m.mu.Lock()
	p, ok := m.live[pid]
	m.mu.Unlock()
	if !ok {
		return false
	}
	p.Close()
	return true
}

// CloseAll gracefully shuts down every live process. Intended for application
// shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	procs := make([]*Process, 0, len(m.live))
	for _, p := range m.live {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	for _, p := range procs {
		p.Close()
	}
}
