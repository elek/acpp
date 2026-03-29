package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"

	"github.com/coder/acp-go-sdk"
)

// Status represents the current state of a session
type Status string

const (
	StatusPending  Status = "pending"
	StatusRunning  Status = "running"
	StatusComplete Status = "complete"
	StatusError    Status = "error"
)

// ACPSession represents an ACP (Agent Client Protocol) agent session.
type ACPSession struct {
	ID         string
	SourceName string // Name of the source channel (e.g., "console", Discord channel name)
	Cwd        string
	Agent      string
	Sandbox    string   // Optional sandbox command to wrap the agent
	Env        []string // Environment variables in KEY=VALUE format
	Status     Status
	CreatedAt  time.Time
	Error      string

	// updates delivers session events (update + session ID) to consumers.
	// The channel is closed when the session finishes.
	updates chan SessionEvent

	// cancel is used to terminate the session's context
	cancel context.CancelFunc

	// mu protects Status and Error fields
	mu sync.RWMutex

	ctx               context.Context
	sess              acp.NewSessionResponse
	conn              *acp.ClientSideConnection
	client            *SessionClient
	permissionHandler PermissionHandler

	// pid stores the subprocess PID once started
	pid int
	// processDone is closed when the subprocess exits
	processDone chan struct{}
	// stdin is the subprocess stdin pipe, closed during graceful shutdown
	stdin io.Closer

	// initResp stores the InitializeResponse for agent info / SDK version
	initResp *acp.InitializeResponse
	// lastModel stores the actual model ID from agent_message_chunk._meta.claudeCode.model
	lastModel string
	// usage accumulates usage data across all prompts
	usage UsageInfo

	// promptLog writes per-prompt JSONL entries to the prompts/ directory
	promptLog     *os.File
	promptLogOnce sync.Once
}

// UsageInfo holds cumulative usage data extracted from ACP response metadata.
type UsageInfo struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	ContextWindow            int64
	MaxOutputTokens          int64
	WebSearchRequests        int64
	CostUSD                  float64
	PromptCount              int64
}

// PromptEntry is a single JSONL record written to the prompts/ directory.
type PromptEntry struct {
	Timestamp string    `json:"timestamp"`
	Prompt    string    `json:"prompt"`
	Usage     UsageInfo `json:"usage"`
}

// NewACPSession creates a new ACP session with the given parameters.
func NewACPSession(id string, opts SessionOpts) *ACPSession {
	ctx, cancel := context.WithCancel(context.Background())
	s := &ACPSession{
		ID:                id,
		SourceName:        opts.Source,
		Cwd:               opts.CWD,
		Agent:             opts.Agent,
		Sandbox:           opts.Sandbox,
		Env:               opts.Env,
		Status:            StatusPending,
		CreatedAt:         time.Now(),
		updates:           make(chan SessionEvent, 64),
		ctx:               ctx,
		cancel:            cancel,
		permissionHandler: opts.PermissionHandler,
	}
	return s
}

// GetStatus returns the current session status (thread-safe)
// GetID returns the session ID.
func (s *ACPSession) GetID() string { return s.ID }

// GetAgent returns the agent command string.
func (s *ACPSession) GetAgent() string { return s.Agent }

// GetCwd returns the working directory.
func (s *ACPSession) GetCwd() string { return s.Cwd }

// GetSandbox returns the sandbox command.
func (s *ACPSession) GetSandbox() string { return s.Sandbox }

// GetEnv returns the environment variables.
func (s *ACPSession) GetEnv() []string { return s.Env }

func (s *ACPSession) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

// SetStatus sets the session status (thread-safe)
func (s *ACPSession) SetStatus(status Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
}

// GetError returns the current session error (thread-safe)
func (s *ACPSession) GetError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Error
}

// SetError sets the session error (thread-safe)
func (s *ACPSession) SetError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Error = err
}

// Close shuts down the session gracefully: it first closes the subprocess
// stdin pipe so the agent sees EOF and can exit on its own. If the agent
// doesn't exit within 5 seconds, it falls back to SIGTERM via context
// cancellation (with cmd.WaitDelay escalating to SIGKILL after another 5s).
func (s *ACPSession) Close() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	processDone := s.processDone
	stdinPipe := s.stdin
	s.stdin = nil
	s.mu.Unlock()

	// Step 1: Close stdin to signal the subprocess that no more input is
	// coming. Well-behaved ACP agents exit when their stdin is closed.
	if stdinPipe != nil {
		stdinPipe.Close()
	}

	// Step 2: Give the subprocess time to exit gracefully.
	if processDone != nil {
		select {
		case <-processDone:
			// Subprocess exited on its own — skip SIGTERM.
			cancel = nil
		case <-time.After(5 * time.Second):
			// Subprocess didn't exit; fall through to SIGTERM.
		}
	}

	// Step 3: If still alive, cancel context → SIGTERM → WaitDelay → SIGKILL.
	if cancel != nil {
		cancel()
	}
	if processDone != nil {
		select {
		case <-processDone:
		case <-time.After(10 * time.Second):
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.Close()
	}
	if s.promptLog != nil {
		s.promptLog.Close()
		s.promptLog = nil
	}
}

// logPrompt writes a PromptEntry to the prompts/ JSONL file.
// The file is created lazily on the first call.
// File name: {sourceName}-{date}-{sessionID}.jsonl
func (s *ACPSession) logPrompt(prompt string) {
	s.promptLogOnce.Do(func() {
		dir := "prompts"
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		date := s.CreatedAt.Format("2006-01-02")
		filename := filepath.Join(dir, s.SourceName+"-"+date+"-"+s.ID+".jsonl")
		s.promptLog, _ = os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	})
	if s.promptLog == nil {
		return
	}

	s.mu.RLock()
	usage := s.usage
	s.mu.RUnlock()

	entry := PromptEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Prompt:    prompt,
		Usage:     usage,
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	s.promptLog.Write(raw)
	s.promptLog.Write([]byte("\n"))
}

// Start begins agent execution with the given prompt
func (s *ACPSession) Start() error {
	s.mu.Lock()
	if s.Status != StatusPending {
		s.mu.Unlock()
		return fmt.Errorf("session already started")
	}
	s.Status = StatusRunning
	s.mu.Unlock()

	go func() {
		err := s.init()
		if err != nil {
			s.setErrorStatus(err)
			close(s.updates) // Signal that session won't be ready (callers should check status/error)
			s.mu.RLock()
			cancel := s.cancel
			s.mu.RUnlock()
			if cancel != nil {
				cancel()
			}
		}
	}()
	return nil
}

// runAgent spawns the agent subprocess and handles ACP communication
func (s *ACPSession) init() error {
	// Spawn agent subprocess, optionally wrapped with sandbox
	args := strings.Fields(s.Agent)
	if s.Sandbox != "" {
		sandboxArgs := strings.Fields(s.Sandbox)
		args = append(sandboxArgs, args...)
	}
	slog.Info("spawning agent", "args", args, "cwd", s.Cwd)
	cmd := exec.CommandContext(s.ctx, args[0], args[1:]...)
	cmd.Dir = s.Cwd // Set working directory so sandbox.sh binds the correct path
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Send SIGTERM to the entire process group so child processes
		// (e.g. inside bwrap sandbox) are also terminated.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	// Set environment variables if provided
	if len(s.Env) > 0 {
		cmd.Env = append(os.Environ(), s.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errors.WithStack(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.WithStack(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return errors.WithStack(err)
	}

	// Capture stderr for logging (e.g., sandbox errors)
	var stderrBuf bytes.Buffer
	go io.Copy(&stderrBuf, stderr)

	if err := cmd.Start(); err != nil {
		return errors.WithStack(err)
	}
	s.mu.Lock()
	s.pid = cmd.Process.Pid
	s.stdin = stdin
	s.mu.Unlock()

	logger := slog.Default().With("session", s.ID)

	// Start cmd.Wait() in a background goroutine immediately so that Go's
	// exec.CommandContext can enforce the SIGTERM → WaitDelay → SIGKILL
	// escalation when the context is cancelled. Without this, cancelling
	// the context sends SIGTERM but never escalates to SIGKILL because
	// Wait() must be actively running for the WaitDelay mechanism to work.
	s.processDone = make(chan struct{})
	processDone := s.processDone
	go func() {
		defer close(processDone)
		waitErr := cmd.Wait()
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

	// stderrSnapshot returns the current stderr output for error messages.
	stderrSnapshot := func() string {
		return strings.TrimSpace(stderrBuf.String())
	}

	// Create ACP connection with SessionClient that sends to the updates channel
	s.client = NewSessionClient(s.ID, s.SourceName, s.Sandbox, s.Cwd, s.emitUpdate, s.permissionHandler)
	s.conn = acp.NewClientSideConnection(s.client, stdin, stdout)

	// Set session-specific logger so SDK messages include the session ID
	s.conn.SetLogger(logger)

	// Initialize protocol
	initResp, err := s.conn.Initialize(s.ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapability{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: false,
		},
	})
	if err != nil {
		<-processDone
		if stderrStr := stderrSnapshot(); stderrStr != "" {
			return fmt.Errorf("%s: %w", stderrStr, err)
		}
		return errors.WithStack(err)
	}
	s.mu.Lock()
	s.initResp = &initResp
	s.mu.Unlock()

	// Create ACP session
	sess, err := s.conn.NewSession(s.ctx, acp.NewSessionRequest{
		Cwd:        s.Cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		<-processDone
		if stderrStr := stderrSnapshot(); stderrStr != "" {
			return fmt.Errorf("%s: %w", stderrStr, err)
		}
		return errors.WithStack(err)
	}
	s.sess = sess
	logger.Info("session connected", "source", s.SourceName, "agent", s.Agent, "cwd", s.Cwd)

	// Send a zero-value event to signal readiness. Consumers receive this
	// first event to know the session is ready for prompts.
	s.updates <- SessionEvent{SessionID: s.ID}

	// Wait for ACP connection to fully drain (all handleInbound goroutines complete)
	// This ensures all session/update notifications are processed before channel close
	<-s.conn.Done()

	// Wait for subprocess to be fully reaped. Since cmd.Wait() is already
	// running in the background, this just waits for it to complete.
	<-processDone

	// Cancel context before closing the channel so any in-flight emitUpdate
	// call takes the ctx.Done() branch instead of sending on a closed channel.
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	close(s.updates)
	return nil
}

// setErrorStatus sets the session to error state with the given error
func (s *ACPSession) setErrorStatus(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = StatusError
	s.Error = err.Error()
}

func (s *ACPSession) Cancel() error {
	err := s.conn.Cancel(s.ctx, acp.CancelNotification{
		SessionId: s.sess.SessionId,
	})
	return err
}

func (s *ACPSession) Prompt(content []acp.ContentBlock) (acp.PromptResponse, error) {
	resp, err := s.conn.Prompt(s.ctx, acp.PromptRequest{
		SessionId: s.sess.SessionId,
		Prompt:    content,
	})
	s.mu.Lock()
	s.usage.PromptCount++
	s.mu.Unlock()
	if err == nil {
		// Use resp.Meta as fallback if streaming chunks didn't provide model/usage
		s.mu.Lock()
		if s.lastModel == "" {
			for _, key := range metaKeys {
				if model := getMetaString(resp.Meta, key, "model"); model != "" {
					s.lastModel = model
					break
				}
			}
		}
		s.mu.Unlock()
		if usage := extractUsageFromMeta(resp.Meta); usage.InputTokens > 0 || usage.OutputTokens > 0 {
			s.mu.Lock()
			usage.PromptCount = s.usage.PromptCount
			s.usage = usage
			s.mu.Unlock()
		}
	}
	s.logPrompt(contentBlocksText(content))
	return resp, err
}

// contentBlocksText extracts a loggable text representation from content blocks.
func contentBlocksText(blocks []acp.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Text != nil {
			parts = append(parts, b.Text.Text)
		} else if b.Image != nil {
			parts = append(parts, fmt.Sprintf("[image: %s]", b.Image.MimeType))
		}
	}
	return strings.Join(parts, " ")
}

// Ready returns the updates channel. Consumers receive a zero-value
// SessionEvent first (signalling readiness), followed by all session
// updates. The channel is closed when the session finishes.
func (s *ACPSession) Ready() <-chan SessionEvent {
	return s.updates
}

// emitUpdate sends an event to the updates channel and tracks internal state.
func (s *ACPSession) emitUpdate(update acp.SessionUpdate) {
	// Extract model and usage from agent_message_chunk._meta (claudeCode or rai)
	if update.AgentMessageChunk != nil {
		meta := update.AgentMessageChunk.Meta
		for _, key := range metaKeys {
			if model := getMetaString(meta, key, "model"); model != "" {
				s.mu.Lock()
				s.lastModel = model
				s.mu.Unlock()
				break
			}
		}
		if usage := extractUsageFromMeta(meta); usage.InputTokens > 0 || usage.OutputTokens > 0 {
			s.mu.Lock()
			usage.PromptCount = s.usage.PromptCount
			s.usage = usage
			s.mu.Unlock()
		}
	}

	select {
	case s.updates <- SessionEvent{SessionID: s.ID, Update: update}:
	case <-s.ctx.Done():
	}
}

// metaKeys are the top-level _meta keys that may contain usage data.
// "claudeCode", "rai", "codex", and "gemini" all use the same inner structure.
var metaKeys = []string{"claudeCode", "rai", "codex", "gemini"}

// extractUsageFromMeta extracts cumulative usage data from the prompt response _meta.
// Accepts usage under either _meta.claudeCode or _meta.rai (same structure).
// We sum across all models within modelUsage.
func extractUsageFromMeta(meta map[string]any) UsageInfo {
	var info UsageInfo
	var section map[string]any
	for _, key := range metaKeys {
		if s, ok := meta[key].(map[string]any); ok {
			section = s
			break
		}
	}
	if section == nil {
		return info
	}
	claudeCode := section

	// Extract totalCostUsd from claudeCode level
	if v, ok := claudeCode["totalCostUsd"].(float64); ok {
		info.CostUSD = v
	}

	modelUsage, ok := claudeCode["modelUsage"].(map[string]any)
	if !ok {
		return info
	}
	for _, usage := range modelUsage {
		m, ok := usage.(map[string]any)
		if !ok {
			continue
		}
		if v, ok := m["inputTokens"].(float64); ok {
			info.InputTokens += int64(v)
		}
		if v, ok := m["outputTokens"].(float64); ok {
			info.OutputTokens += int64(v)
		}
		if v, ok := m["cacheCreationInputTokens"].(float64); ok {
			info.CacheCreationInputTokens += int64(v)
		}
		if v, ok := m["cacheReadInputTokens"].(float64); ok {
			info.CacheReadInputTokens += int64(v)
		}
		if v, ok := m["contextWindow"].(float64); ok {
			info.ContextWindow = int64(v)
		}
		if v, ok := m["maxOutputTokens"].(float64); ok {
			info.MaxOutputTokens = int64(v)
		}
		if v, ok := m["webSearchRequests"].(float64); ok {
			info.WebSearchRequests += int64(v)
		}
		if v, ok := m["costUSD"].(float64); ok && info.CostUSD == 0 {
			info.CostUSD += v
		}
	}
	return info
}

// getMetaString extracts a nested string from a _meta map.
// e.g. getMetaString(meta, "claudeCode", "model") gets meta["claudeCode"]["model"].
func getMetaString(meta map[string]any, keys ...string) string {
	if meta == nil || len(keys) == 0 {
		return ""
	}
	current := any(meta)
	for i, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		val, exists := m[key]
		if !exists {
			return ""
		}
		if i == len(keys)-1 {
			if s, ok := val.(string); ok {
				return s
			}
			return ""
		}
		current = val
	}
	return ""
}

// getMetaFloat extracts a nested float64 from a _meta map.
func getMetaFloat(meta map[string]any, keys ...string) (float64, bool) {
	if meta == nil || len(keys) == 0 {
		return 0, false
	}
	current := any(meta)
	for i, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		val, exists := m[key]
		if !exists {
			return 0, false
		}
		if i == len(keys)-1 {
			if f, ok := val.(float64); ok {
				return f, true
			}
			return 0, false
		}
		current = val
	}
	return 0, false
}

// StatusInfo holds the extended status information for display.
type StatusInfo struct {
	SessionOpts
	Status     Status
	CreatedAt  time.Time
	SDKVersion string
	Model      string
	PID        int
	Usage      UsageInfo
	HasUsage   bool
}

// FormatUsageSummary returns a human-readable one-line summary of token usage.
// Returns empty string if no usage data is available.
func (info StatusInfo) FormatUsageSummary() string {
	if !info.HasUsage {
		return ""
	}
	u := info.Usage
	parts := []string{fmt.Sprintf("💬 %d", u.PromptCount)}
	parts = append(parts, fmt.Sprintf("📥 %d / 📤 %d", u.InputTokens, u.OutputTokens))
	if u.CacheCreationInputTokens > 0 || u.CacheReadInputTokens > 0 {
		parts = append(parts, fmt.Sprintf("📦 %d+%d", u.CacheCreationInputTokens, u.CacheReadInputTokens))
	}
	if u.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("💰 $%.4f", u.CostUSD))
	}
	return strings.Join(parts, " | ")
}

// GetStatusInfo returns extended status information for display.
func (s *ACPSession) GetStatusInfo() StatusInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info := StatusInfo{
		SessionOpts: SessionOpts{
			Source:  s.SourceName,
			Agent:   s.Agent,
			CWD:     s.Cwd,
			Sandbox: s.Sandbox,
			Env:     s.Env,
		},
		Status:    s.Status,
		CreatedAt: s.CreatedAt,
		Model:     s.lastModel,
		PID:       s.pid,
	}

	// Extract SDK version from initResp.agentInfo._meta (claudeCode or rai)
	if s.initResp != nil && s.initResp.AgentInfo != nil {
		for _, key := range metaKeys {
			if v := getMetaString(s.initResp.AgentInfo.Meta, key, "sdkVersion"); v != "" {
				info.SDKVersion = v
				break
			}
		}
	}

	// Use cumulative usage across all prompts
	if s.usage.InputTokens > 0 || s.usage.OutputTokens > 0 {
		info.Usage = s.usage
		info.HasUsage = true
	}

	return info
}

// Context returns the session's context for lifecycle coordination.
func (s *ACPSession) Context() context.Context {
	return s.ctx
}

// GetModels returns the session's model state (available models and current model).
func (s *ACPSession) GetModels() []string {
	return []string{}
}

// GetModes returns the session's mode state (available modes and current mode).
func (s *ACPSession) GetModes() *acp.SessionModeState {
	return s.sess.Modes
}

// SetMode sets the session's current mode.
func (s *ACPSession) SetMode(modeId string) error {
	_, err := s.conn.SetSessionMode(s.ctx, acp.SetSessionModeRequest{
		SessionId: s.sess.SessionId,
		ModeId:    acp.SessionModeId(modeId),
	})
	return err
}
