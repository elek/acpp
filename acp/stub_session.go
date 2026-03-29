package acp

import (
	"context"
	"time"

	"github.com/coder/acp-go-sdk"
)

// StubSession is a fake session for integration testing.
// After receiving a prompt it emits a sequence of canned SessionEvents
// (tool calls + text chunks) and returns a static PromptResponse.
type StubSession struct {
	id      string
	opts    SessionOpts
	status  Status
	updates chan SessionEvent
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewStubSession creates a new stub session.
func NewStubSession(id string, opts SessionOpts) *StubSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &StubSession{
		id:      id,
		opts:    opts,
		status:  StatusPending,
		updates: make(chan SessionEvent, 64),
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (s *StubSession) Start() error {
	s.status = StatusRunning
	// Signal readiness with a zero-value event, matching ACPSession convention.
	s.updates <- SessionEvent{SessionID: s.id}
	return nil
}

func (s *StubSession) Prompt(content []acp.ContentBlock) (acp.PromptResponse, error) {
	toolKind := acp.ToolKindRead
	toolStatus := acp.ToolCallStatusCompleted

	// Emit a tool call event.
	s.updates <- SessionEvent{
		SessionID: s.id,
		Update: acp.SessionUpdate{
			ToolCall: &acp.SessionUpdateToolCall{
				ToolCallId: "tool-1",
				Title:      "Read file",
				Kind:       toolKind,
				Status:     toolStatus,
				RawInput:   map[string]interface{}{"path": "/tmp/hello.txt"},
			},
		},
	}

	// Emit text chunks.
	for _, chunk := range []string{"Hello", ", ", "world!"} {
		s.updates <- SessionEvent{
			SessionID: s.id,
			Update: acp.SessionUpdate{
				AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: chunk}},
				},
			},
		}
	}

	return acp.PromptResponse{
		StopReason: acp.StopReasonEndTurn,
	}, nil
}

func (s *StubSession) Ready() <-chan SessionEvent { return s.updates }

func (s *StubSession) Close() {
	s.status = StatusComplete
	close(s.updates)
	s.cancel()
}

func (s *StubSession) Cancel() error { return nil }

func (s *StubSession) GetStatus() Status    { return s.status }
func (s *StubSession) GetError() string      { return "" }
func (s *StubSession) GetID() string         { return s.id }
func (s *StubSession) GetAgent() string      { return s.opts.Agent }
func (s *StubSession) GetCwd() string        { return s.opts.CWD }
func (s *StubSession) GetSandbox() string    { return s.opts.Sandbox }
func (s *StubSession) GetEnv() []string      { return s.opts.Env }
func (s *StubSession) Context() context.Context { return s.ctx }

func (s *StubSession) GetModes() *acp.SessionModeState { return nil }
func (s *StubSession) SetMode(modeId string) error      { return nil }

func (s *StubSession) GetStatusInfo() StatusInfo {
	return StatusInfo{
		SessionOpts: s.opts,
		Status:      s.status,
		CreatedAt:   time.Now(),
	}
}
