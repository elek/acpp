package types

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/sandbox"
)

// SessionOpts holds the common parameters for a session, shared across all backends.
// Used both for session creation (NewSession) and embedded in StatusInfo.
type SessionOpts struct {
	ProjectID string
	Agent     string
	CWD       string
	Env       []string
	Sandbox   sandbox.Sandbox
	// Source identifies the channel that created the session ("web", "discord",
	// "console", "cli"); recorded as the session's source_name.
	Source string
	// SandboxType is the sandbox type string ("bbwrap", "none", …) used to create
	// Sandbox, retained for persistence since sandbox.Sandbox exposes no name.
	SandboxType string
}

// SessionEvent pairs a session update with the session ID that produced it.
type SessionEvent struct {
	SessionID string
	Update    acp.SessionUpdate
}

// Session is the interface for interacting with an AI agent process/sesssion.
type Session interface {
	Start() error
	Prompt(content []acp.ContentBlock) (acp.PromptResponse, error)
	Ready() <-chan SessionEvent
	Close()
	Cancel() error

	GetStatus() Status
	GetError() string
	GetStatusInfo() StatusInfo

	GetModes() *acp.SessionModeState
	SetMode(modeId string) error

	GetID() string
	// GetACPSessionID returns the protocol-level ACP session id (for resume).
	GetACPSessionID() string
	GetAgent() string
	GetCwd() string
	GetSandbox() string
	GetSandboxProfiles() string
	GetEnv() []string

	Context() context.Context
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

// Status represents the current state of a session
type Status string

const (
	StatusPending  Status = "pending"
	StatusRunning  Status = "running"
	StatusComplete Status = "complete"
	StatusError    Status = "error"
)

// UsageInfo holds cumulative usage data extracted from ACP response metadata.
type UsageInfo struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	ContextWindow            int64
	ContextUsed              int64
	MaxOutputTokens          int64
	WebSearchRequests        int64
	CostUSD                  float64
	PromptCount              int64
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
