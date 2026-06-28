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

// HandleCommands interprets a control input issued in the conversation
// identified by id: a leading-slash command (/cancel, /clear, /exit) or a
// single-line "!" shell escape that runs the rest of the line inside the
// conversation's sandbox. It returns handled=false when text is neither, leaving
// the caller to treat it as a normal prompt. Any feedback (e.g. shell output) is
// fanned to subscribers through the router rather than returned here.
func (r *Router) HandleCommands(ctx context.Context, id types.ConversationMeta, text string) (handled bool, err error) {
	text = strings.TrimSpace(text)

	// A single line prefixed with "!" is a shell escape: run the rest of the line
	// inside the conversation's sandbox (the same one the ACP process runs in) and
	// surface its output. Multi-line input is left to be treated as a prompt.
	if strings.HasPrefix(text, "!") && !strings.ContainsRune(text, '\n') {
		return r.handleShell(ctx, id, strings.TrimPrefix(text, "!"))
	}

	if !strings.HasPrefix(text, "/") {
		return false, nil
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
			return true, fmt.Errorf("router: unknown conversation %v", id)
		}
		if err := r.Send(ctx, id, acp.CancelNotification{SessionId: sessionID}); err != nil {
			return true, err
		}
		return true, nil
	case "/clear":
		// Restart the conversation's session, discarding prior context. The
		// resulting ConversationReplaced event lets channels re-point their
		// mapping and surface feedback to the user.
		if _, err := r.Restart(ctx, id); err != nil {
			return true, err
		}
		return true, nil
	case "/exit":
		// Bring the whole application down via the registered shutdown hook
		// (typically the main context's cancel func).
		r.mu.RLock()
		shutdown := r.shutdown
		r.mu.RUnlock()
		if shutdown != nil {
			shutdown()
		}
		return true, nil
	case "/help":
		return r.handleHelp(ctx, id)
	}

	return false, nil
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

// handleHelp fans back a listing of the commands available in this conversation:
// first the commands the agent advertised (if any), then the built-in harness
// commands. The listing is surfaced as an agent message so every channel renders
// it the same way it renders the agent's own output.
func (r *Router) handleHelp(ctx context.Context, id types.ConversationMeta) (handled bool, err error) {
	r.mu.RLock()
	state, ok := r.sessions[id.ConversationID]
	var agentCmds []acp.AvailableCommand
	var meta types.ConversationMeta
	if ok {
		agentCmds = state.availableCommands
		meta = state.meta
	}
	r.mu.RUnlock()
	if !ok {
		return true, fmt.Errorf("router: unknown conversation %v", id)
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

	r.Receive(ctx, nil, meta, acp.SessionNotification{
		SessionId: meta.SessionID,
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Content:       acp.TextBlock(b.String()),
				SessionUpdate: "agent_message_chunk",
			},
		},
	})
	return true, nil
}

// handleShell runs command inside the conversation's sandbox and fans the
// command line together with its combined output back through the router as an
// agent message, so every subscribed channel renders it the same way it renders
// the agent's own messages. An empty command is consumed but runs nothing.
func (r *Router) handleShell(ctx context.Context, id types.ConversationMeta, command string) (handled bool, err error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return true, nil
	}

	r.mu.RLock()
	state, ok := r.sessions[id.ConversationID]
	var opts types.SessionOpts
	var meta types.ConversationMeta
	if ok {
		opts = state.opts
		meta = state.meta
	}
	r.mu.RUnlock()
	if !ok {
		return true, fmt.Errorf("router: unknown conversation %v", id)
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

	r.Receive(ctx, nil, meta, acp.SessionNotification{
		SessionId: meta.SessionID,
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Content:       acp.TextBlock(b.String()),
				SessionUpdate: "agent_message_chunk",
			},
		},
	})
	return true, nil
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
