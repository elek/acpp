package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/coder/acp-go-sdk"
)

// SessionClient implements acp.Client for session-based streaming to a channel
type SessionClient struct {
	sessionID         string
	sourceName        string
	sandbox           string                         // optional sandbox command to wrap acpp read
	cwd               string                         // working directory for sandbox execution
	emit              func(update acp.SessionUpdate) // callback for multi-consumer event delivery
	permissionHandler PermissionHandler              // called for tool permission requests
	logFile           *os.File
	logMu             sync.Mutex
}

// NewSessionClient creates a new SessionClient that sends events to the given channel
// and optionally calls an emit callback for subscriber notification.
// If permissionHandler is nil, all tool calls are auto-approved.
// sandbox and cwd are used to delegate ReadTextFile to `acpp read` inside the sandbox.
func NewSessionClient(sessionID string, sourceName string, sandbox string, cwd string, emit func(update acp.SessionUpdate), permissionHandler PermissionHandler) *SessionClient {
	if permissionHandler == nil {
		permissionHandler = AllowAll
	}
	return &SessionClient{sessionID: sessionID, sourceName: sourceName, sandbox: sandbox, cwd: cwd, emit: emit, permissionHandler: permissionHandler}
}

// Close closes the log file if open
func (c *SessionClient) Close() error {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	if c.logFile != nil {
		err := c.logFile.Close()
		c.logFile = nil
		return err
	}
	return nil
}

// debugSessionUpdate returns a detailed string representation of a SessionUpdate
// for debugging marshal failures. It tries to marshal each non-nil field individually.
func debugSessionUpdate(u acp.SessionUpdate) string {
	fields := []struct {
		name string
		val  any
	}{
		{"UserMessageChunk", u.UserMessageChunk},
		{"AgentMessageChunk", u.AgentMessageChunk},
		{"AgentThoughtChunk", u.AgentThoughtChunk},
		{"ToolCall", u.ToolCall},
		{"ToolCallUpdate", u.ToolCallUpdate},
		{"Plan", u.Plan},
		{"AvailableCommandsUpdate", u.AvailableCommandsUpdate},
		{"CurrentModeUpdate", u.CurrentModeUpdate},
	}
	var parts []string
	for _, f := range fields {
		if f.val == nil {
			continue
		}
		b, err := json.Marshal(f.val)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%s(marshal err: %v): %+v", f.name, err, f.val))
		} else {
			parts = append(parts, fmt.Sprintf("%s: %s", f.name, string(b)))
		}
	}
	return strings.Join(parts, "; ")
}

func (c *SessionClient) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	u := n.Update

	// Save to file: log/<sessionID>.json (one JSON object per line)
	if c.sessionID != "" {
		raw, err := json.Marshal(u)
		if err != nil {
			// SDK can't round-trip certain content block types; log a warning
			// and skip the file entry rather than writing empty/corrupt data.
			slog.Warn("failed to marshal session update for file log", "session", c.sessionID, "err", err, "update", debugSessionUpdate(u))
		} else {
			c.logMu.Lock()
			if c.logFile == nil {
				logDir := "log"
				if err := os.MkdirAll(logDir, 0o755); err == nil {
					filename := filepath.Join(logDir, c.sourceName+"-"+c.sessionID+".json")
					c.logFile, _ = os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
				}
			}
			if c.logFile != nil {
				c.logFile.Write(raw)
				c.logFile.Write([]byte("\n"))
			}
			c.logMu.Unlock()
		}
	}

	if c.emit != nil {
		c.emit(u)
	}
	return nil
}

func (c *SessionClient) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return c.permissionHandler(ctx, p)
}

func (c *SessionClient) ReadTextFile(ctx context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if !filepath.IsAbs(p.Path) {
		return acp.ReadTextFileResponse{}, fmt.Errorf("path must be absolute: %s", p.Path)
	}

	// Build acpp read command args
	args := []string{"read"}
	if p.Line != nil {
		args = append(args, "--line", strconv.Itoa(*p.Line))
	}
	if p.Limit != nil {
		args = append(args, "--limit", strconv.Itoa(*p.Limit))
	}
	args = append(args, p.Path)

	// TODO: this one doesn't work with `go run.
	//// Wrap with sandbox if configured
	//exe, err := os.Executable()
	//if err != nil {
	//	return acp.ReadTextFileResponse{}, fmt.Errorf("resolve executable: %w", err)
	//}
	cmdArgs := append([]string{"acpp"}, args...)
	if c.sandbox != "" {
		sandboxArgs := strings.Fields(c.sandbox)
		cmdArgs = append(sandboxArgs, cmdArgs...)
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir = c.cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("acpp read %s: %s: %w", p.Path, strings.TrimSpace(stderr.String()), err)
	}
	return acp.ReadTextFileResponse{Content: stdout.String()}, nil
}

func (c *SessionClient) WriteTextFile(ctx context.Context, p acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if !filepath.IsAbs(p.Path) {
		return acp.WriteTextFileResponse{}, fmt.Errorf("path must be absolute: %s", p.Path)
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	return acp.WriteTextFileResponse{}, os.WriteFile(p.Path, []byte(p.Content), 0o644)
}

// Terminal stubs - not implemented for session-based execution
func (c *SessionClient) CreateTerminal(ctx context.Context, p acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{TerminalId: "stub-terminal"}, nil
}

func (c *SessionClient) KillTerminalCommand(ctx context.Context, p acp.KillTerminalCommandRequest) (acp.KillTerminalCommandResponse, error) {
	return acp.KillTerminalCommandResponse{}, nil
}

func (c *SessionClient) ReleaseTerminal(ctx context.Context, p acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}

func (c *SessionClient) TerminalOutput(ctx context.Context, p acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{Output: "", Truncated: false}, nil
}

func (c *SessionClient) WaitForTerminalExit(ctx context.Context, p acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}
