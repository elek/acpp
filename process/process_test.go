package process

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/stretchr/testify/require"
)

// TestProcessPipesAndClose spawns `cat` (which echoes stdin to stdout) to verify
// that Start wires up the pipes and that Close shuts the process down via stdin
// EOF without needing a signal.
func TestProcessPipesAndClose(t *testing.T) {
	m := NewManager()
	proc, err := m.Start(context.Background(), Spec{Agent: "cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if proc.PID() <= 0 {
		t.Fatalf("expected a positive PID, got %d", proc.PID())
	}

	if _, err := io.WriteString(proc.Stdin, "hello\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}

	line, err := bufio.NewReader(proc.Stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if line != "hello\n" {
		t.Fatalf("expected echoed %q, got %q", "hello\n", line)
	}

	// Closing stdin gives cat EOF; it should exit on its own (no SIGTERM).
	proc.Close()

	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit after Close")
	}
}

// TestClaude spawns the real claude-code-acp agent through the process Manager
// and drives a full ACP prompt round trip over the Manager-provided pipes.
func TestClaude(t *testing.T) {
	const bin = "/home/elek/.npm-global/bin/claude-code-acp"
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("agent binary not available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	m := NewManager()
	proc, err := m.Start(ctx, Spec{Agent: bin, Cwd: cwd})
	require.NoError(t, err)
	defer proc.Close()
	require.Positive(t, proc.PID())

	// The connection delivers every inbound message through the client callback,
	// including the responses to our outbound requests. Coordinate the async
	// handshake (initialize -> session/new -> prompt) over these channels.
	var (
		mu         sync.Mutex
		agentText  strings.Builder
		stopReason acp.StopReason
	)
	var (
		initDone   = make(chan struct{}, 1)
		sessDone   = make(chan acp.NewSessionResponse, 1)
		promptDone = make(chan struct{}, 1)
	)
	client := func(ctx context.Context, rid *json.RawMessage, msg any) {
		switch m := msg.(type) {
		case acp.InitializeResponse:
			initDone <- struct{}{}
		case acp.NewSessionResponse:
			sessDone <- m
		case acp.SessionNotification:
			switch u := m.Update; {
			case u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil:
				text := u.AgentMessageChunk.Content.Text.Text
				mu.Lock()
				agentText.WriteString(text)
				mu.Unlock()
				t.Logf("agent_message_chunk: %s", text)
			case u.AgentThoughtChunk != nil && u.AgentThoughtChunk.Content.Text != nil:
				t.Logf("agent_thought_chunk: %s", u.AgentThoughtChunk.Content.Text.Text)
			case u.ToolCall != nil:
				t.Logf("tool_call: %s", u.ToolCall.Title)
			default:
				t.Logf("session/update for %s", m.SessionId)
			}
		case acp.PromptResponse:
			mu.Lock()
			stopReason = m.StopReason
			mu.Unlock()
			promptDone <- struct{}{}
		default:
			t.Logf("inbound: %T", msg)
		}
	}

	// The Manager exposes the agent's stdin (writer) and stdout (reader).
	c := acp.NewClientSideConnection(client, proc.Stdin, proc.Stdout)

	require.NoError(t, c.Send(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapability{ReadTextFile: true, WriteTextFile: true},
		},
	}))
	select {
	case <-initDone:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for initialize response: %v", ctx.Err())
	}

	require.NoError(t, c.Send(ctx, acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	}))
	var sess acp.NewSessionResponse
	select {
	case sess = <-sessDone:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for session/new response: %v", ctx.Err())
	}
	require.NotEmpty(t, sess.SessionId)

	require.NoError(t, c.Send(ctx, acp.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("What is the capital of Spain?")},
	}))
	select {
	case <-promptDone:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for prompt response: %v", ctx.Err())
	}

	mu.Lock()
	full := agentText.String()
	t.Logf("stop reason: %s", stopReason)
	mu.Unlock()
	t.Logf("full agent response:\n%s", full)
	require.Contains(t, full, "Madrid")
}

// TestCloseAllTerminatesLiveProcess verifies CloseAll shuts down a process that
// would otherwise run indefinitely, falling back to SIGTERM.
func TestCloseAllTerminatesLiveProcess(t *testing.T) {
	m := NewManager()
	// `sleep 60` ignores stdin EOF, so Close must escalate to SIGTERM.
	if _, err := m.Start(context.Background(), Spec{Agent: "sleep 60"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		m.CloseAll()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(12 * time.Second):
		t.Fatal("CloseAll did not return")
	}
}
