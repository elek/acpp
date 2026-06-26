package acp

import (
	"encoding/json"
	"errors"
)

// ProtocolVersionNumber is the ACP protocol version implemented by this package.
const ProtocolVersionNumber = 1

// ProtocolVersion is the protocol version identifier exchanged during initialize.
type ProtocolVersion int

// Method names sent by the client to the agent.
const (
	AgentMethodInitialize     = "initialize"
	AgentMethodAuthenticate   = "authenticate"
	AgentMethodSessionNew     = "session/new"
	AgentMethodSessionLoad    = "session/load"
	AgentMethodSessionPrompt  = "session/prompt"
	AgentMethodSessionCancel  = "session/cancel"
	AgentMethodSessionSetMode = "session/set_mode"
)

// Method names the client handles on behalf of the agent.
const (
	ClientMethodFsReadTextFile           = "fs/read_text_file"
	ClientMethodFsWriteTextFile          = "fs/write_text_file"
	ClientMethodSessionRequestPermission = "session/request_permission"
	ClientMethodSessionUpdate            = "session/update"
	ClientMethodTerminalCreate           = "terminal/create"
	ClientMethodTerminalKill             = "terminal/kill"
	ClientMethodTerminalOutput           = "terminal/output"
	ClientMethodTerminalRelease          = "terminal/release"
	ClientMethodTerminalWaitForExit      = "terminal/wait_for_exit"
)

// ---- Identifiers and enums ----

// SessionId identifies a protocol-level session.
type SessionId string

// SessionModeId is the unique identifier of a session mode.
type SessionModeId string

// ToolCallId identifies a tool call within a session.
type ToolCallId string

// PermissionOptionId identifies a permission option.
type PermissionOptionId string

// PermissionOptionKind hints at the nature of a permission option.
type PermissionOptionKind string

const (
	PermissionOptionKindAllowOnce    PermissionOptionKind = "allow_once"
	PermissionOptionKindAllowAlways  PermissionOptionKind = "allow_always"
	PermissionOptionKindRejectOnce   PermissionOptionKind = "reject_once"
	PermissionOptionKindRejectAlways PermissionOptionKind = "reject_always"
)

// ToolKind categorizes the kind of tool being invoked.
type ToolKind string

const (
	ToolKindRead       ToolKind = "read"
	ToolKindEdit       ToolKind = "edit"
	ToolKindDelete     ToolKind = "delete"
	ToolKindMove       ToolKind = "move"
	ToolKindSearch     ToolKind = "search"
	ToolKindExecute    ToolKind = "execute"
	ToolKindThink      ToolKind = "think"
	ToolKindFetch      ToolKind = "fetch"
	ToolKindSwitchMode ToolKind = "switch_mode"
	ToolKindOther      ToolKind = "other"
)

// ToolCallStatus is the execution status of a tool call.
type ToolCallStatus string

const (
	ToolCallStatusPending    ToolCallStatus = "pending"
	ToolCallStatusInProgress ToolCallStatus = "in_progress"
	ToolCallStatusCompleted  ToolCallStatus = "completed"
	ToolCallStatusFailed     ToolCallStatus = "failed"
)

// PlanEntryPriority is the relative importance of a plan entry.
type PlanEntryPriority string

const (
	PlanEntryPriorityHigh   PlanEntryPriority = "high"
	PlanEntryPriorityMedium PlanEntryPriority = "medium"
	PlanEntryPriorityLow    PlanEntryPriority = "low"
)

// PlanEntryStatus tracks the lifecycle of a plan entry.
type PlanEntryStatus string

const (
	PlanEntryStatusPending    PlanEntryStatus = "pending"
	PlanEntryStatusInProgress PlanEntryStatus = "in_progress"
	PlanEntryStatusCompleted  PlanEntryStatus = "completed"
)

// StopReason explains why an agent stopped processing a prompt turn.
type StopReason string

const (
	StopReasonEndTurn         StopReason = "end_turn"
	StopReasonMaxTokens       StopReason = "max_tokens"
	StopReasonMaxTurnRequests StopReason = "max_turn_requests"
	StopReasonRefusal         StopReason = "refusal"
	StopReasonCancelled       StopReason = "cancelled"
)

// ---- Content blocks (tagged union on "type") ----

// ContentBlockText is plain or Markdown text content.
type ContentBlockText struct {
	Meta map[string]any `json:"_meta,omitempty"`
	Text string         `json:"text"`
	Type string         `json:"type"`
}

// ContentBlockImage is an inline image with base64-encoded data.
type ContentBlockImage struct {
	Meta     map[string]any `json:"_meta,omitempty"`
	Data     string         `json:"data"`
	MimeType string         `json:"mimeType"`
	Type     string         `json:"type"`
	Uri      *string        `json:"uri,omitempty"`
}

// ContentBlockAudio is inline audio with base64-encoded data.
type ContentBlockAudio struct {
	Meta     map[string]any `json:"_meta,omitempty"`
	Data     string         `json:"data"`
	MimeType string         `json:"mimeType"`
	Type     string         `json:"type"`
}

// ContentBlockResourceLink references a resource the agent can access.
type ContentBlockResourceLink struct {
	Meta        map[string]any `json:"_meta,omitempty"`
	Description *string        `json:"description,omitempty"`
	MimeType    *string        `json:"mimeType,omitempty"`
	Name        string         `json:"name"`
	Size        *int           `json:"size,omitempty"`
	Title       *string        `json:"title,omitempty"`
	Type        string         `json:"type"`
	Uri         string         `json:"uri"`
}

// ContentBlockResource embeds complete resource contents directly.
type ContentBlockResource struct {
	Meta     map[string]any  `json:"_meta,omitempty"`
	Resource json.RawMessage `json:"resource"`
	Type     string          `json:"type"`
}

// ContentBlock is a tagged union of the content block variants. Exactly one
// field is non-nil.
type ContentBlock struct {
	Text         *ContentBlockText         `json:"-"`
	Image        *ContentBlockImage        `json:"-"`
	Audio        *ContentBlockAudio        `json:"-"`
	ResourceLink *ContentBlockResourceLink `json:"-"`
	Resource     *ContentBlockResource     `json:"-"`
}

func (u ContentBlock) MarshalJSON() ([]byte, error) {
	switch {
	case u.Text != nil:
		return marshalUnionVariant(u.Text, "type", "text")
	case u.Image != nil:
		return marshalUnionVariant(u.Image, "type", "image")
	case u.Audio != nil:
		return marshalUnionVariant(u.Audio, "type", "audio")
	case u.ResourceLink != nil:
		return marshalUnionVariant(u.ResourceLink, "type", "resource_link")
	case u.Resource != nil:
		return marshalUnionVariant(u.Resource, "type", "resource")
	}
	return []byte("null"), nil
}

func (u *ContentBlock) UnmarshalJSON(b []byte) error {
	switch readDiscriminator(b, "type") {
	case "text":
		var v ContentBlockText
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Text = &v
	case "image":
		var v ContentBlockImage
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Image = &v
	case "audio":
		var v ContentBlockAudio
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Audio = &v
	case "resource_link":
		var v ContentBlockResourceLink
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.ResourceLink = &v
	case "resource":
		var v ContentBlockResource
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Resource = &v
	default:
		return errors.New("acp: unknown content block type")
	}
	return nil
}

// ---- Tool call content (tagged union on "type") ----

// ToolCallContentContent wraps a standard content block.
type ToolCallContentContent struct {
	Meta    map[string]any `json:"_meta,omitempty"`
	Content ContentBlock   `json:"content"`
	Type    string         `json:"type"`
}

// ToolCallContentDiff is a file modification shown as a diff.
type ToolCallContentDiff struct {
	Meta    map[string]any `json:"_meta,omitempty"`
	NewText string         `json:"newText"`
	OldText *string        `json:"oldText,omitempty"`
	Path    string         `json:"path"`
	Type    string         `json:"type"`
}

// ToolCallContentTerminal embeds a terminal by id.
type ToolCallContentTerminal struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	TerminalId string         `json:"terminalId"`
	Type       string         `json:"type"`
}

// ToolCallContent is a tagged union of tool call content variants.
type ToolCallContent struct {
	Content  *ToolCallContentContent  `json:"-"`
	Diff     *ToolCallContentDiff     `json:"-"`
	Terminal *ToolCallContentTerminal `json:"-"`
}

func (u ToolCallContent) MarshalJSON() ([]byte, error) {
	switch {
	case u.Content != nil:
		return marshalUnionVariant(u.Content, "type", "content")
	case u.Diff != nil:
		return marshalUnionVariant(u.Diff, "type", "diff")
	case u.Terminal != nil:
		return marshalUnionVariant(u.Terminal, "type", "terminal")
	}
	return []byte("null"), nil
}

func (u *ToolCallContent) UnmarshalJSON(b []byte) error {
	switch readDiscriminator(b, "type") {
	case "content":
		var v ToolCallContentContent
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Content = &v
	case "diff":
		var v ToolCallContentDiff
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Diff = &v
	case "terminal":
		var v ToolCallContentTerminal
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Terminal = &v
	default:
		return errors.New("acp: unknown tool call content type")
	}
	return nil
}

// ToolCallLocation is a file location accessed or modified by a tool.
type ToolCallLocation struct {
	Meta map[string]any `json:"_meta,omitempty"`
	Line *int           `json:"line,omitempty"`
	Path string         `json:"path"`
}

// ---- Plan ----

// PlanEntry is a single task in an execution plan.
type PlanEntry struct {
	Meta     map[string]any    `json:"_meta,omitempty"`
	Content  string            `json:"content"`
	Priority PlanEntryPriority `json:"priority"`
	Status   PlanEntryStatus   `json:"status"`
}

// ---- Session modes ----

// SessionMode describes a mode the agent can operate in.
type SessionMode struct {
	Meta        map[string]any `json:"_meta,omitempty"`
	Description *string        `json:"description,omitempty"`
	Id          SessionModeId  `json:"id"`
	Name        string         `json:"name"`
}

// SessionModeState is the set of modes and the active one.
type SessionModeState struct {
	Meta           map[string]any `json:"_meta,omitempty"`
	AvailableModes []SessionMode  `json:"availableModes"`
	CurrentModeId  SessionModeId  `json:"currentModeId"`
}

// ---- Available commands ----

// AvailableCommandInput is the input specification for a command.
type AvailableCommandInput struct {
	Hint *string `json:"hint,omitempty"`
}

// AvailableCommand is a command the agent can execute.
type AvailableCommand struct {
	Meta        map[string]any         `json:"_meta,omitempty"`
	Description string                 `json:"description"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
	Name        string                 `json:"name"`
}

// ---- Usage / cost ----

// Cost is the cumulative session cost.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// Usage is the token usage reported for a turn.
type Usage struct {
	CachedReadTokens  *int `json:"cachedReadTokens,omitempty"`
	CachedWriteTokens *int `json:"cachedWriteTokens,omitempty"`
	InputTokens       int  `json:"inputTokens"`
	OutputTokens      int  `json:"outputTokens"`
	ThoughtTokens     *int `json:"thoughtTokens,omitempty"`
	TotalTokens       int  `json:"totalTokens"`
}

// ---- Session update (tagged union on "sessionUpdate") ----

// SessionUpdateUserMessageChunk streams a chunk of the user's message.
type SessionUpdateUserMessageChunk struct {
	Meta          map[string]any `json:"_meta,omitempty"`
	Content       ContentBlock   `json:"content"`
	SessionUpdate string         `json:"sessionUpdate"`
}

// SessionUpdateAgentMessageChunk streams a chunk of the agent's response.
type SessionUpdateAgentMessageChunk struct {
	Meta          map[string]any `json:"_meta,omitempty"`
	Content       ContentBlock   `json:"content"`
	SessionUpdate string         `json:"sessionUpdate"`
}

// SessionUpdateAgentThoughtChunk streams a chunk of the agent's reasoning.
type SessionUpdateAgentThoughtChunk struct {
	Meta          map[string]any `json:"_meta,omitempty"`
	Content       ContentBlock   `json:"content"`
	SessionUpdate string         `json:"sessionUpdate"`
}

// SessionUpdateToolCall announces a new tool call.
type SessionUpdateToolCall struct {
	Meta          map[string]any     `json:"_meta,omitempty"`
	Content       []ToolCallContent  `json:"content,omitempty"`
	Kind          ToolKind           `json:"kind,omitempty"`
	Locations     []ToolCallLocation `json:"locations,omitempty"`
	RawInput      any                `json:"rawInput,omitempty"`
	RawOutput     any                `json:"rawOutput,omitempty"`
	SessionUpdate string             `json:"sessionUpdate"`
	Status        ToolCallStatus     `json:"status,omitempty"`
	Title         string             `json:"title"`
	ToolCallId    ToolCallId         `json:"toolCallId"`
}

// SessionToolCallUpdate reports progress/results of an existing tool call.
type SessionToolCallUpdate struct {
	Meta          map[string]any     `json:"_meta,omitempty"`
	Content       []ToolCallContent  `json:"content,omitempty"`
	Kind          *ToolKind          `json:"kind,omitempty"`
	Locations     []ToolCallLocation `json:"locations,omitempty"`
	RawInput      any                `json:"rawInput,omitempty"`
	RawOutput     any                `json:"rawOutput,omitempty"`
	SessionUpdate string             `json:"sessionUpdate"`
	Status        *ToolCallStatus    `json:"status,omitempty"`
	Title         *string            `json:"title,omitempty"`
	ToolCallId    ToolCallId         `json:"toolCallId"`
}

// SessionUpdatePlan reports the agent's execution plan.
type SessionUpdatePlan struct {
	Meta          map[string]any `json:"_meta,omitempty"`
	Entries       []PlanEntry    `json:"entries"`
	SessionUpdate string         `json:"sessionUpdate"`
}

// SessionAvailableCommandsUpdate reports the available commands.
type SessionAvailableCommandsUpdate struct {
	Meta              map[string]any     `json:"_meta,omitempty"`
	AvailableCommands []AvailableCommand `json:"availableCommands"`
	SessionUpdate     string             `json:"sessionUpdate"`
}

// SessionCurrentModeUpdate reports a change of the current session mode.
type SessionCurrentModeUpdate struct {
	Meta          map[string]any `json:"_meta,omitempty"`
	CurrentModeId SessionModeId  `json:"currentModeId"`
	SessionUpdate string         `json:"sessionUpdate"`
}

// SessionUsageUpdate reports context window and cost (unstable extension).
type SessionUsageUpdate struct {
	Meta          map[string]any `json:"_meta,omitempty"`
	Cost          *Cost          `json:"cost,omitempty"`
	SessionUpdate string         `json:"sessionUpdate"`
	Size          int            `json:"size"`
	Used          int            `json:"used"`
}

// SessionUpdate is a tagged union of session update variants. Exactly one
// field is non-nil.
type SessionUpdate struct {
	UserMessageChunk        *SessionUpdateUserMessageChunk  `json:"-"`
	AgentMessageChunk       *SessionUpdateAgentMessageChunk `json:"-"`
	AgentThoughtChunk       *SessionUpdateAgentThoughtChunk `json:"-"`
	ToolCall                *SessionUpdateToolCall          `json:"-"`
	ToolCallUpdate          *SessionToolCallUpdate          `json:"-"`
	Plan                    *SessionUpdatePlan              `json:"-"`
	AvailableCommandsUpdate *SessionAvailableCommandsUpdate `json:"-"`
	CurrentModeUpdate       *SessionCurrentModeUpdate       `json:"-"`
	UsageUpdate             *SessionUsageUpdate             `json:"-"`
}

func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	switch {
	case u.UserMessageChunk != nil:
		return marshalUnionVariant(u.UserMessageChunk, "sessionUpdate", "user_message_chunk")
	case u.AgentMessageChunk != nil:
		return marshalUnionVariant(u.AgentMessageChunk, "sessionUpdate", "agent_message_chunk")
	case u.AgentThoughtChunk != nil:
		return marshalUnionVariant(u.AgentThoughtChunk, "sessionUpdate", "agent_thought_chunk")
	case u.ToolCall != nil:
		return marshalUnionVariant(u.ToolCall, "sessionUpdate", "tool_call")
	case u.ToolCallUpdate != nil:
		return marshalUnionVariant(u.ToolCallUpdate, "sessionUpdate", "tool_call_update")
	case u.Plan != nil:
		return marshalUnionVariant(u.Plan, "sessionUpdate", "plan")
	case u.AvailableCommandsUpdate != nil:
		return marshalUnionVariant(u.AvailableCommandsUpdate, "sessionUpdate", "available_commands_update")
	case u.CurrentModeUpdate != nil:
		return marshalUnionVariant(u.CurrentModeUpdate, "sessionUpdate", "current_mode_update")
	case u.UsageUpdate != nil:
		return marshalUnionVariant(u.UsageUpdate, "sessionUpdate", "usage_update")
	}
	return []byte("null"), nil
}

func (u *SessionUpdate) UnmarshalJSON(b []byte) error {
	switch readDiscriminator(b, "sessionUpdate") {
	case "user_message_chunk":
		var v SessionUpdateUserMessageChunk
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.UserMessageChunk = &v
	case "agent_message_chunk":
		var v SessionUpdateAgentMessageChunk
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.AgentMessageChunk = &v
	case "agent_thought_chunk":
		var v SessionUpdateAgentThoughtChunk
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.AgentThoughtChunk = &v
	case "tool_call":
		var v SessionUpdateToolCall
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.ToolCall = &v
	case "tool_call_update":
		var v SessionToolCallUpdate
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.ToolCallUpdate = &v
	case "plan":
		var v SessionUpdatePlan
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Plan = &v
	case "available_commands_update":
		var v SessionAvailableCommandsUpdate
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.AvailableCommandsUpdate = &v
	case "current_mode_update":
		var v SessionCurrentModeUpdate
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.CurrentModeUpdate = &v
	case "usage_update":
		var v SessionUsageUpdate
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.UsageUpdate = &v
	default:
		// Unknown / future update variant: ignore rather than error so the
		// stream is not interrupted by extension updates we don't model.
		return nil
	}
	return nil
}

// SessionNotification carries a session update from the agent.
type SessionNotification struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	SessionId SessionId      `json:"sessionId"`
	Update    SessionUpdate  `json:"update"`
}

func (v *SessionNotification) Validate() error { return nil }

// ---- Capabilities and implementation info ----

// FileSystemCapability declares which fs methods the client supports.
type FileSystemCapability struct {
	Meta          map[string]any `json:"_meta,omitempty"`
	ReadTextFile  bool           `json:"readTextFile,omitempty"`
	WriteTextFile bool           `json:"writeTextFile,omitempty"`
}

// ClientCapabilities declares what the client supports.
type ClientCapabilities struct {
	Meta     map[string]any       `json:"_meta,omitempty"`
	Fs       FileSystemCapability `json:"fs,omitempty"`
	Terminal bool                 `json:"terminal,omitempty"`
}

// Implementation describes the name/version of a client or agent.
type Implementation struct {
	Meta    map[string]any `json:"_meta,omitempty"`
	Name    string         `json:"name"`
	Title   *string        `json:"title,omitempty"`
	Version string         `json:"version"`
}

// ---- MCP server (sent by the client; minimal stdio form) ----

// EnvVariable is a name/value environment entry.
type EnvVariable struct {
	Meta  map[string]any `json:"_meta,omitempty"`
	Name  string         `json:"name"`
	Value string         `json:"value"`
}

// McpServer describes an MCP server the agent should connect to.
type McpServer struct {
	Name    string        `json:"name,omitempty"`
	Command string        `json:"command,omitempty"`
	Args    []string      `json:"args,omitempty"`
	Env     []EnvVariable `json:"env,omitempty"`
}

// ---- initialize ----

// InitializeRequest establishes the connection and negotiates capabilities.
type InitializeRequest struct {
	Meta               map[string]any     `json:"_meta,omitempty"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities,omitempty"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
	ProtocolVersion    ProtocolVersion    `json:"protocolVersion"`
}

// InitializeResponse returns the negotiated protocol version and agent info.
type InitializeResponse struct {
	Meta              map[string]any  `json:"_meta,omitempty"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities,omitempty"`
	AgentInfo         *Implementation `json:"agentInfo,omitempty"`
	AuthMethods       json.RawMessage `json:"authMethods,omitempty"`
	ProtocolVersion   ProtocolVersion `json:"protocolVersion"`
}

// ---- session/new ----

// NewSessionRequest creates a new session.
type NewSessionRequest struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	Cwd        string         `json:"cwd"`
	McpServers []McpServer    `json:"mcpServers"`
}

// NewSessionResponse returns the created session id and initial mode state.
type NewSessionResponse struct {
	Meta      map[string]any    `json:"_meta,omitempty"`
	Modes     *SessionModeState `json:"modes,omitempty"`
	SessionId SessionId         `json:"sessionId"`
}

// ---- session/load ----

// LoadSessionRequest resumes an existing session.
type LoadSessionRequest struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	Cwd        string         `json:"cwd"`
	McpServers []McpServer    `json:"mcpServers"`
	SessionId  SessionId      `json:"sessionId"`
}

// LoadSessionResponse returns the loaded session's initial mode state.
type LoadSessionResponse struct {
	Meta  map[string]any    `json:"_meta,omitempty"`
	Modes *SessionModeState `json:"modes,omitempty"`
}

// ---- session/prompt ----

// PromptRequest sends a user prompt to the agent.
type PromptRequest struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	Prompt    []ContentBlock `json:"prompt"`
	SessionId SessionId      `json:"sessionId"`
}

// PromptResponse is returned once the prompt turn completes.
type PromptResponse struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	StopReason StopReason     `json:"stopReason"`
	Usage      *Usage         `json:"usage,omitempty"`
}

// ---- session/cancel ----

// CancelNotification cancels operations for a session.
type CancelNotification struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	SessionId SessionId      `json:"sessionId"`
}

// ---- session/set_mode ----

// SetSessionModeRequest sets the current mode of a session.
type SetSessionModeRequest struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	ModeId    SessionModeId  `json:"modeId"`
	SessionId SessionId      `json:"sessionId"`
}

// SetSessionModeResponse is the (empty) response to session/set_mode.
type SetSessionModeResponse struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

// ---- permission ----

// PermissionOption is an option presented to the user.
type PermissionOption struct {
	Meta     map[string]any       `json:"_meta,omitempty"`
	Kind     PermissionOptionKind `json:"kind"`
	Name     string               `json:"name"`
	OptionId PermissionOptionId   `json:"optionId"`
}

// ToolCallUpdate describes a tool call (as included in a permission request).
type ToolCallUpdate struct {
	Meta       map[string]any     `json:"_meta,omitempty"`
	Content    []ToolCallContent  `json:"content,omitempty"`
	Kind       *ToolKind          `json:"kind,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   any                `json:"rawInput,omitempty"`
	RawOutput  any                `json:"rawOutput,omitempty"`
	Status     *ToolCallStatus    `json:"status,omitempty"`
	Title      *string            `json:"title,omitempty"`
	ToolCallId ToolCallId         `json:"toolCallId"`
}

// RequestPermissionRequest asks for permission to run a tool call.
type RequestPermissionRequest struct {
	Meta      map[string]any     `json:"_meta,omitempty"`
	Options   []PermissionOption `json:"options"`
	SessionId SessionId          `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
}

func (v *RequestPermissionRequest) Validate() error { return nil }

// RequestPermissionOutcomeCancelled means the turn was cancelled before a choice.
type RequestPermissionOutcomeCancelled struct {
	Outcome string `json:"outcome"`
}

// RequestPermissionOutcomeSelected means the user picked an option.
type RequestPermissionOutcomeSelected struct {
	Meta     map[string]any     `json:"_meta,omitempty"`
	OptionId PermissionOptionId `json:"optionId"`
	Outcome  string             `json:"outcome"`
}

// RequestPermissionOutcome is a tagged union on "outcome".
type RequestPermissionOutcome struct {
	Cancelled *RequestPermissionOutcomeCancelled `json:"-"`
	Selected  *RequestPermissionOutcomeSelected  `json:"-"`
}

func (u RequestPermissionOutcome) MarshalJSON() ([]byte, error) {
	switch {
	case u.Cancelled != nil:
		return marshalUnionVariant(u.Cancelled, "outcome", "cancelled")
	case u.Selected != nil:
		return marshalUnionVariant(u.Selected, "outcome", "selected")
	}
	return []byte("null"), nil
}

func (u *RequestPermissionOutcome) UnmarshalJSON(b []byte) error {
	switch readDiscriminator(b, "outcome") {
	case "cancelled":
		var v RequestPermissionOutcomeCancelled
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Cancelled = &v
	case "selected":
		var v RequestPermissionOutcomeSelected
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Selected = &v
	default:
		return errors.New("acp: unknown permission outcome")
	}
	return nil
}

// RequestPermissionResponse is the client's decision on a permission request.
type RequestPermissionResponse struct {
	Meta    map[string]any           `json:"_meta,omitempty"`
	Outcome RequestPermissionOutcome `json:"outcome"`
}

// ---- file system ----

// ReadTextFileRequest asks the client to read a text file.
type ReadTextFileRequest struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	Limit     *int           `json:"limit,omitempty"`
	Line      *int           `json:"line,omitempty"`
	Path      string         `json:"path"`
	SessionId SessionId      `json:"sessionId"`
}

func (v *ReadTextFileRequest) Validate() error { return nil }

// ReadTextFileResponse returns file contents.
type ReadTextFileResponse struct {
	Meta    map[string]any `json:"_meta,omitempty"`
	Content string         `json:"content"`
}

// WriteTextFileRequest asks the client to write a text file.
type WriteTextFileRequest struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	Content   string         `json:"content"`
	Path      string         `json:"path"`
	SessionId SessionId      `json:"sessionId"`
}

func (v *WriteTextFileRequest) Validate() error { return nil }

// WriteTextFileResponse is the (empty) response to fs/write_text_file.
type WriteTextFileResponse struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

// ---- terminals ----

// CreateTerminalRequest asks the client to create a terminal.
type CreateTerminalRequest struct {
	Meta            map[string]any `json:"_meta,omitempty"`
	Args            []string       `json:"args,omitempty"`
	Command         string         `json:"command"`
	Cwd             *string        `json:"cwd,omitempty"`
	Env             []EnvVariable  `json:"env,omitempty"`
	OutputByteLimit *int           `json:"outputByteLimit,omitempty"`
	SessionId       SessionId      `json:"sessionId"`
}

func (v *CreateTerminalRequest) Validate() error { return nil }

// CreateTerminalResponse returns the id of a created terminal.
type CreateTerminalResponse struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	TerminalId string         `json:"terminalId"`
}

// KillTerminalCommandRequest kills a terminal command.
type KillTerminalCommandRequest struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	SessionId  SessionId      `json:"sessionId"`
	TerminalId string         `json:"terminalId"`
}

func (v *KillTerminalCommandRequest) Validate() error { return nil }

// KillTerminalCommandResponse is the (empty) response to terminal/kill.
type KillTerminalCommandResponse struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

// ReleaseTerminalRequest releases a terminal.
type ReleaseTerminalRequest struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	SessionId  SessionId      `json:"sessionId"`
	TerminalId string         `json:"terminalId"`
}

func (v *ReleaseTerminalRequest) Validate() error { return nil }

// ReleaseTerminalResponse is the (empty) response to terminal/release.
type ReleaseTerminalResponse struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

// TerminalExitStatus is the exit status of a terminal command.
type TerminalExitStatus struct {
	Meta     map[string]any `json:"_meta,omitempty"`
	ExitCode *int           `json:"exitCode,omitempty"`
	Signal   *string        `json:"signal,omitempty"`
}

// TerminalOutputRequest gets current terminal output.
type TerminalOutputRequest struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	SessionId  SessionId      `json:"sessionId"`
	TerminalId string         `json:"terminalId"`
}

func (v *TerminalOutputRequest) Validate() error { return nil }

// TerminalOutputResponse returns terminal output and exit status.
type TerminalOutputResponse struct {
	Meta       map[string]any      `json:"_meta,omitempty"`
	ExitStatus *TerminalExitStatus `json:"exitStatus,omitempty"`
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
}

// WaitForTerminalExitRequest waits for a terminal command to exit.
type WaitForTerminalExitRequest struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	SessionId  SessionId      `json:"sessionId"`
	TerminalId string         `json:"terminalId"`
}

func (v *WaitForTerminalExitRequest) Validate() error { return nil }

// WaitForTerminalExitResponse returns the exit status of a terminal command.
type WaitForTerminalExitResponse struct {
	Meta     map[string]any `json:"_meta,omitempty"`
	ExitCode *int           `json:"exitCode,omitempty"`
	Signal   *string        `json:"signal,omitempty"`
}

// ---- union (de)serialization helpers ----

// marshalUnionVariant marshals v, then injects the discriminator key so it is
// present even when the variant struct's own discriminator field was left
// empty by the caller.
func marshalUnionVariant(v any, discKey, discVal string) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	dv, err := json.Marshal(discVal)
	if err != nil {
		return nil, err
	}
	m[discKey] = dv
	return json.Marshal(m)
}

// readDiscriminator extracts the string value of key from a JSON object,
// returning "" if absent or not a string.
func readDiscriminator(b []byte, key string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// ---- content block helpers ----

// TextBlock constructs a text content block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Text: &ContentBlockText{Text: text, Type: "text"}}
}

// ImageBlock constructs an inline image content block with base64 data.
func ImageBlock(data string, mimeType string) ContentBlock {
	return ContentBlock{Image: &ContentBlockImage{Data: data, MimeType: mimeType, Type: "image"}}
}

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }
