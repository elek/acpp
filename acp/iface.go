package acp

import (
	"context"
	"strings"

	"github.com/coder/acp-go-sdk"
)

// SessionOpts holds the common parameters for a session, shared across all backends.
// Used both for session creation (NewSession) and embedded in StatusInfo.
type SessionOpts struct {
	Source            string
	Agent             string
	CWD               string
	Sandbox           string
	Env               []string
	PermissionHandler PermissionHandler `json:"-" yaml:"-"` // optional; nil means auto-approve
}

// SessionEvent pairs a session update with the session ID that produced it.
type SessionEvent struct {
	SessionID string
	Update    acp.SessionUpdate
}

// Session is the interface for interacting with an AI agent session.
// Both ACP and OpenCode backends implement this interface.
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
	GetAgent() string
	GetCwd() string
	GetSandbox() string
	GetEnv() []string

	Context() context.Context
}

// NewSession creates a new session, routing to the appropriate backend
// based on the agent string. If agent starts with "opencode", an OpenCode
// HTTP session is created; otherwise an ACP subprocess session is used.
func NewSession(id string, opts SessionOpts) Session {
	if strings.HasPrefix(opts.Agent, "<STUB>") {
		return NewStubSession(id, opts)
	}
	if strings.HasPrefix(opts.Agent, "@opencode") {
		return NewOpenCodeSession(id, opts)
	}
	return NewACPSession(id, opts)
}
