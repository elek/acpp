package acp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaude(t *testing.T) {
	const bin = "/home/elek/.npm-global/bin/claude-code-acp"
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("agent binary not available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, bin)

	// The connection writes to the agent's stdin and reads from its stdout, so we
	// need real pipes (the bare exec.Cmd fields are nil until wired up).
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	command.Stderr = os.Stderr

	require.NoError(t, command.Start())
	defer func() {
		cancel()
		_ = command.Wait()
	}()

	// The connection delivers every inbound message through the client callback,
	// including the responses to our outbound requests. Coordinate the async
	// handshake (initialize -> session/new -> prompt) over these channels.
	var (
		mu         sync.Mutex
		agentText  strings.Builder
		stopReason StopReason
	)
	var (
		initDone   = make(chan struct{}, 1)
		sessDone   = make(chan NewSessionResponse, 1)
		promptDone = make(chan struct{}, 1)
	)
	client := func(ctx context.Context, rid *json.RawMessage, msg any) {
		switch m := msg.(type) {
		case InitializeResponse:
			initDone <- struct{}{}
		case NewSessionResponse:
			sessDone <- m
		case SessionNotification:
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
		case PromptResponse:
			mu.Lock()
			stopReason = m.StopReason
			mu.Unlock()
			promptDone <- struct{}{}
		default:
			t.Logf("inbound: %T", msg)
		}
	}

	// peerInput is the agent's stdin (what we write to); peerOutput is the
	// agent's stdout (what we read from).
	c := NewClientSideConnection(client, stdin, stdout)

	require.NoError(t, c.Send(ctx, InitializeRequest{
		ProtocolVersion: ProtocolVersionNumber,
		ClientCapabilities: ClientCapabilities{
			Fs: FileSystemCapability{ReadTextFile: true, WriteTextFile: true},
		},
	}))
	select {
	case <-initDone:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for initialize response: %v", ctx.Err())
	}

	cwd, err := os.Getwd()
	require.NoError(t, err)

	require.NoError(t, c.Send(ctx, NewSessionRequest{
		Cwd:        cwd,
		McpServers: []McpServer{},
	}))
	var sess NewSessionResponse
	select {
	case sess = <-sessDone:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for session/new response: %v", ctx.Err())
	}
	require.NotEmpty(t, sess.SessionId)

	require.NoError(t, c.Send(ctx, PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []ContentBlock{TextBlock("What is the capital of Spain?")},
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
