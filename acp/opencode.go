package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/coder/acp-go-sdk"
)

// OpenCodeSession implements Session using the OpenCode HTTP API.
type OpenCodeSession struct {
	id         string
	sourceName string
	cwd        string
	agent      string
	env        []string

	// updates delivers session events (update + session ID) to consumers.
	// The channel is closed when the session finishes.
	updates chan SessionEvent

	baseURL   string
	sessionID string // OpenCode session ID (from POST /session)
	status    Status
	err       string
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.RWMutex

	cmd         *exec.Cmd      // opencode serve subprocess (nil if connecting to existing)
	processDone chan struct{}   // closed when subprocess exits
	usage       UsageInfo
	lastModel   string
	createdAt   time.Time
	client      *http.Client
	sseCancel   context.CancelFunc
}

// NewOpenCodeSession creates a new OpenCode session.
func NewOpenCodeSession(id string, opts SessionOpts) *OpenCodeSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &OpenCodeSession{
		id:         id,
		sourceName: opts.Source,
		cwd:        opts.CWD,
		agent:      opts.Agent,
		env:        opts.Env,
		updates:    make(chan SessionEvent, 64),
		baseURL:    "http://127.0.0.1:4096",
		status:     StatusPending,
		createdAt:  time.Now(),
		ctx:        ctx,
		cancel:     cancel,
		client:     &http.Client{},
	}
}

func (s *OpenCodeSession) GetID() string      { return s.id }
func (s *OpenCodeSession) GetAgent() string    { return s.agent }
func (s *OpenCodeSession) GetCwd() string      { return s.cwd }
func (s *OpenCodeSession) GetSandbox() string  { return "" }
func (s *OpenCodeSession) GetEnv() []string    { return s.env }
func (s *OpenCodeSession) Context() context.Context { return s.ctx }

func (s *OpenCodeSession) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *OpenCodeSession) GetError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

func (s *OpenCodeSession) GetModes() *acp.SessionModeState {
	return nil
}

func (s *OpenCodeSession) SetMode(modeId string) error {
	return fmt.Errorf("modes not supported for opencode sessions")
}

func (s *OpenCodeSession) GetStatusInfo() StatusInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var pid int
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	info := StatusInfo{
		SessionOpts: SessionOpts{
			Source: s.sourceName,
			Agent:  s.agent,
			CWD:    s.cwd,
			Env:    s.env,
		},
		Status:    s.status,
		CreatedAt: s.createdAt,
		Model:     s.lastModel,
		PID:       pid,
	}
	if s.usage.InputTokens > 0 || s.usage.OutputTokens > 0 {
		info.Usage = s.usage
		info.HasUsage = true
	}
	return info
}

func (s *OpenCodeSession) Ready() <-chan SessionEvent {
	return s.updates
}

func (s *OpenCodeSession) Start() error {
	s.mu.Lock()
	if s.status != StatusPending {
		s.mu.Unlock()
		return fmt.Errorf("session already started")
	}
	s.status = StatusRunning
	s.mu.Unlock()

	go func() {
		if err := s.init(); err != nil {
			s.mu.Lock()
			s.status = StatusError
			s.err = err.Error()
			s.mu.Unlock()
			close(s.updates)
			s.cancel()
		}
	}()
	return nil
}

func (s *OpenCodeSession) init() error {
	logger := slog.Default().With("session", s.id, "backend", "opencode")

	// Start opencode serve as subprocess
	cmd := exec.CommandContext(s.ctx, "opencode", "serve")
	cmd.Dir = s.cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	if len(s.env) > 0 {
		cmd.Env = append(os.Environ(), s.env...)
	}

	// Capture stderr for error reporting
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start opencode serve: %w", err)
	}
	s.cmd = cmd
	s.processDone = make(chan struct{})
	logger.Info("started opencode serve subprocess", "pid", cmd.Process.Pid)

	// Start cmd.Wait() immediately so Go can enforce SIGTERM → SIGKILL escalation.
	go func() {
		defer close(s.processDone)
		if err := cmd.Wait(); err != nil {
			logger.Warn("opencode serve exited with error", "error", err)
		} else {
			logger.Info("opencode serve exited normally")
		}
	}()

	// Poll health endpoint until ready
	if err := s.waitForHealth(logger); err != nil {
		return fmt.Errorf("opencode serve failed to become healthy: %w", err)
	}

	// Start SSE event stream
	sseCtx, sseCancel := context.WithCancel(s.ctx)
	s.sseCancel = sseCancel

	sse := &openCodeSSE{
		baseURL:   s.baseURL,
		sessionID: "", // will be set after session creation
		emit:      s.emitUpdate,
		client:    &http.Client{}, // no timeout for SSE
		logger:    logger,
		onMessage: s.handleMessageUpdate,
		onStatusChange: func(status ocSessionState) {
			logger.Debug("session status changed", "status", status.Type)
		},
	}

	// Create session via API
	sessionID, err := s.createSession()
	if err != nil {
		sseCancel()
		return fmt.Errorf("failed to create opencode session: %w", err)
	}
	s.sessionID = sessionID
	sse.sessionID = sessionID
	logger.Info("opencode session created", "sessionID", sessionID)

	// Start SSE in background; close updates channel when SSE finishes.
	go func() {
		if err := sse.run(sseCtx); err != nil && sseCtx.Err() == nil {
			logger.Warn("SSE stream error", "error", err)
		}
		close(s.updates)
	}()

	// Send a zero-value event to signal readiness.
	s.updates <- SessionEvent{SessionID: s.id}
	return nil
}

// waitForHealth polls the health endpoint until the server is ready.
func (s *OpenCodeSession) waitForHealth(logger *slog.Logger) error {
	deadline := time.After(30 * time.Second)
	interval := 100 * time.Millisecond

	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for opencode serve to become healthy")
		default:
		}

		resp, err := s.client.Get(s.baseURL + "/global/health")
		if err == nil {
			var health struct {
				Healthy bool   `json:"healthy"`
				Version string `json:"version"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&health); err == nil && health.Healthy {
				resp.Body.Close()
				logger.Info("opencode serve is healthy", "version", health.Version)
				return nil
			}
			resp.Body.Close()
		}

		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(interval):
		}
		// Exponential backoff capped at 2s
		if interval < 2*time.Second {
			interval = interval * 3 / 2
		}
	}
}

// ocSessionResponse is the response from POST /session.
type ocSessionResponse struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectID"`
	Title     string `json:"title"`
}

// createSession creates a new session via the OpenCode API.
func (s *OpenCodeSession) createSession() (string, error) {
	body := bytes.NewBufferString("{}")
	resp, err := s.client.Post(s.baseURL+"/session", "application/json", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create session returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var session ocSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return "", fmt.Errorf("failed to decode session response: %w", err)
	}
	return session.ID, nil
}

// ocMessageRequest is the request body for POST /session/:id/message.
type ocMessageRequest struct {
	Parts []ocMessagePart `json:"parts"`
}

// ocMessagePart is a part in a message request.
type ocMessagePart struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	MediaType string `json:"mediaType,omitempty"` // MIME type for image parts
	Data      string `json:"data,omitempty"`      // base64-encoded data for image parts
}

// ocMessageResponse is the response from POST /session/:id/message.
type ocMessageResponse struct {
	Info  ocMessage `json:"info"`
	Parts []ocPart  `json:"parts"`
}

func (s *OpenCodeSession) Prompt(content []acp.ContentBlock) (acp.PromptResponse, error) {
	var parts []ocMessagePart
	for _, block := range content {
		switch {
		case block.Text != nil:
			parts = append(parts, ocMessagePart{Type: "text", Text: block.Text.Text})
		case block.Image != nil:
			parts = append(parts, ocMessagePart{
				Type:      "image",
				MediaType: block.Image.MimeType,
				Data:      block.Image.Data,
			})
		}
	}
	reqBody := ocMessageRequest{
		Parts: parts,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return acp.PromptResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/session/%s/message", s.baseURL, s.sessionID)
	req, err := http.NewRequestWithContext(s.ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return acp.PromptResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return acp.PromptResponse{}, fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return acp.PromptResponse{}, fmt.Errorf("message returned %d: %s", resp.StatusCode, string(respBody))
	}

	var msgResp ocMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&msgResp); err != nil {
		return acp.PromptResponse{}, fmt.Errorf("decode response: %w", err)
	}

	// Update usage from assistant message
	s.updateUsageFromMessage(msgResp.Info)

	return acp.PromptResponse{
		StopReason: acp.StopReasonEndTurn,
	}, nil
}

func (s *OpenCodeSession) Cancel() error {
	url := fmt.Sprintf("%s/session/%s/abort", s.baseURL, s.sessionID)
	req, err := http.NewRequestWithContext(s.ctx, "POST", url, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("abort session: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (s *OpenCodeSession) Close() {
	if s.sseCancel != nil {
		s.sseCancel()
	}
	// Cancelling the context triggers cmd.Cancel (SIGTERM) for the subprocess.
	if s.cancel != nil {
		s.cancel()
	}
	// Wait for the subprocess to actually exit so we don't leak processes.
	// cmd.WaitDelay (5s) ensures SIGKILL after SIGTERM, so 10s is enough.
	if s.processDone != nil {
		select {
		case <-s.processDone:
		case <-time.After(10 * time.Second):
		}
	}
}

// emitUpdate sends an event to the updates channel.
func (s *OpenCodeSession) emitUpdate(update acp.SessionUpdate) {
	select {
	case s.updates <- SessionEvent{SessionID: s.id, Update: update}:
	case <-s.ctx.Done():
	}
}

// handleMessageUpdate processes message.updated events from SSE.
// Only used for model tracking; usage is tracked from the synchronous Prompt response.
func (s *OpenCodeSession) handleMessageUpdate(msg ocMessage) {
	if msg.Role != "assistant" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if msg.ModelID != "" {
		s.lastModel = msg.ModelID
	}
}

// updateUsageFromMessage updates cumulative usage from a synchronous message response.
func (s *OpenCodeSession) updateUsageFromMessage(msg ocMessage) {
	if msg.Role != "assistant" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.usage.PromptCount++

	if msg.ModelID != "" {
		s.lastModel = msg.ModelID
	}

	if msg.Tokens != nil {
		s.usage.InputTokens += msg.Tokens.Input
		s.usage.OutputTokens += msg.Tokens.Output
		s.usage.CacheReadInputTokens += msg.Tokens.Cache.Read
		s.usage.CacheCreationInputTokens += msg.Tokens.Cache.Write
	}
	if msg.Cost > 0 {
		s.usage.CostUSD += msg.Cost
	}
}
