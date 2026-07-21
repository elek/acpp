package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/sandbox"
	"github.com/elek/acpp/types"
)

// harnessSlashCommands is the set of leading-slash commands the harness itself
// consumes. Anything else starting with "/" (e.g. an agent-advertised command
// like /review) falls through to be sent to the agent as a prompt.
var harnessSlashCommands = map[string]bool{
	"/cancel": true,
	"/clear":  true,
	"/exit":   true,
	"/help":   true,
}

// IsCommand reports whether text (after trimming) is a harness command the
// caller should surface specially rather than send to the agent: a recognised
// leading-slash command or a single-line "!" shell escape. Callers use this to
// decide whether to echo the input as a control action before dispatching it via
// HandleCommands.
func IsCommand(text string) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "!") && !strings.ContainsRune(text, '\n') {
		return true
	}
	fields := strings.Fields(text)
	return len(fields) > 0 && harnessSlashCommands[fields[0]]
}

// HandleCommands interprets a control input issued in the conversation
// identified by id: a leading-slash command (/cancel, /clear, /exit, /help) or a
// single-line "!" shell escape that runs the rest of the line inside the
// conversation's sandbox. It returns handled=false when text is neither, leaving
// the caller to treat it as a normal prompt. When handled, any textual feedback
// (shell output, the /help listing, a confirmation) is returned for the caller
// to surface however it renders control output — it is deliberately not fanned
// through the router, so command output stays transient and is never persisted.
func (r *Router) HandleCommands(ctx context.Context, id types.ConversationMeta, text string) (handled bool, feedback string, err error) {
	text = strings.TrimSpace(text)

	// A single line prefixed with "!" is a shell escape: run the rest of the line
	// inside the conversation's sandbox (the same one the ACP process runs in) and
	// surface its output. Multi-line input is left to be treated as a prompt.
	if strings.HasPrefix(text, "!") && !strings.ContainsRune(text, '\n') {
		return r.handleShell(ctx, id, strings.TrimPrefix(text, "!"))
	}

	if !strings.HasPrefix(text, "/") {
		return false, "", nil
	}

	switch strings.Fields(text)[0] {
	case "/cancel":
		// Resolve the up-to-date session id from router state rather than trusting
		// the caller's (possibly pre-handshake) meta.
		r.mu.RLock()
		state, ok := r.sessions[id.ConversationID]
		var sessionID acp.SessionId
		if ok {
			sessionID = state.meta.SessionID
		}
		r.mu.RUnlock()
		if !ok {
			return true, "", fmt.Errorf("router: unknown conversation %v", id)
		}
		if err := r.Send(ctx, id, acp.CancelNotification{SessionId: sessionID}); err != nil {
			return true, "", err
		}
		return true, "Cancelled the in-flight turn.", nil
	case "/clear":
		// Restart the conversation's session, discarding prior context. The
		// resulting ConversationReplaced event lets channels re-point their
		// mapping (the web UI navigates to the fresh session), so no textual
		// feedback is needed here.
		if _, err := r.Restart(ctx, id); err != nil {
			return true, "", err
		}
		return true, "", nil
	case "/exit":
		// Bring the whole application down via the registered shutdown hook
		// (typically the main context's cancel func).
		r.mu.RLock()
		shutdown := r.shutdown
		r.mu.RUnlock()
		if shutdown != nil {
			shutdown()
		}
		return true, "Shutting down…", nil
	case "/help":
		return r.handleHelp(ctx, id)
	}

	return false, "", nil
}

// harnessCommands is the static list of commands the harness itself handles,
// used to render /help. It is display-only; dispatch happens in the switch above.
var harnessCommands = []struct{ Name, Desc string }{
	{"/cancel", "Cancel the in-flight agent turn"},
	{"/clear", "Restart the session, discarding prior context"},
	{"/exit", "Shut the application down"},
	{"/help", "List available commands"},
	{"!<command>", "Run a shell command in the conversation's sandbox"},
}

// handleHelp returns a listing of the commands available in this conversation:
// first the commands the agent advertised (if any), then the built-in harness
// commands. The listing is returned as feedback for the caller to render however
// it surfaces control output.
func (r *Router) handleHelp(ctx context.Context, id types.ConversationMeta) (handled bool, feedback string, err error) {
	r.mu.RLock()
	state, ok := r.sessions[id.ConversationID]
	var agentCmds []acp.AvailableCommand
	if ok {
		agentCmds = state.availableCommands
	}
	r.mu.RUnlock()
	if !ok {
		return true, "", fmt.Errorf("router: unknown conversation %v", id)
	}

	var b strings.Builder
	if len(agentCmds) > 0 {
		b.WriteString("Agent commands:\n")
		for _, c := range agentCmds {
			fmt.Fprintf(&b, "  /%-14s %s\n", c.Name, c.Description)
		}
		b.WriteByte('\n')
	}
	b.WriteString("Harness commands:\n")
	for _, c := range harnessCommands {
		fmt.Fprintf(&b, "  %-15s %s\n", c.Name, c.Desc)
	}

	return true, b.String(), nil
}

// handleShell runs command inside the conversation's sandbox and returns the
// command line together with its combined output as feedback for the caller to
// render however it surfaces control output. An empty command is consumed but
// runs nothing.
func (r *Router) handleShell(ctx context.Context, id types.ConversationMeta, command string) (handled bool, feedback string, err error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return true, "", nil
	}

	r.mu.RLock()
	state, ok := r.sessions[id.ConversationID]
	var opts types.SessionOpts
	if ok {
		opts = state.opts
	}
	r.mu.RUnlock()
	if !ok {
		return true, "", fmt.Errorf("router: unknown conversation %v", id)
	}

	output, runErr := runInSandbox(ctx, opts.Sandbox, opts.CWD, opts.Env, command)

	var b strings.Builder
	fmt.Fprintf(&b, "$ %s\n", command)
	b.WriteString(output)
	if output != "" && !strings.HasSuffix(output, "\n") {
		b.WriteByte('\n')
	}
	if runErr != nil {
		fmt.Fprintf(&b, "[exit: %v]\n", runErr)
	}

	return true, b.String(), nil
}

// runInSandbox executes a single shell command line via "sh -c", wrapped in sb
// when one is configured (nil means run unsandboxed), in working directory cwd
// with env appended to the ambient environment. It returns the command's
// combined stdout+stderr along with any exit error.
func runInSandbox(ctx context.Context, sb sandbox.Sandbox, cwd string, env []string, command string) (string, error) {
	name, args := "sh", []string{"-c", command}
	if sb != nil {
		name, args = sb.Wrap(name, args)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
