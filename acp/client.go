package acp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
)

type Client func(ctx context.Context, id *json.RawMessage, msg any)

// ClientSideConnection is the client's view of an ACP connection. It dispatches
// inbound agent->client calls to the provided Client and exposes the outbound
// client->agent methods.
type ClientSideConnection struct {
	conn   *Connection
	client Client
}

// NewClientSideConnection creates a client-side connection bound to client,
// communicating over the given stdio streams (peerInput is the agent's stdin,
// peerOutput is the agent's stdout).
func NewClientSideConnection(client Client, peerInput io.Writer, peerOutput io.Reader) *ClientSideConnection {
	c := &ClientSideConnection{client: client}
	c.conn = NewConnection(c.handle, peerInput, peerOutput)
	return c
}

// Done is closed when the peer disconnects.
func (c *ClientSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the given logger.
func (c *ClientSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }

// handle dispatches inbound agent->client methods to the Client implementation.
func (c *ClientSideConnection) handle(ctx context.Context, id *json.RawMessage, method string, params json.RawMessage) *RequestError {
	switch method {
	// ---- responses to our outbound agent requests (id-less by the time they
	// reach here; Connection has already correlated them to their method) ----
	case AgentMethodInitialize:
		var p InitializeResponse
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case AgentMethodSessionNew:
		var p NewSessionResponse
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case AgentMethodSessionLoad:
		var p LoadSessionResponse
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case AgentMethodSessionPrompt:
		var p PromptResponse
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case AgentMethodSessionSetMode:
		var p SetSessionModeResponse
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	// ---- inbound requests/notifications from the agent ----
	case ClientMethodFsReadTextFile:
		var p ReadTextFileRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case ClientMethodFsWriteTextFile:
		var p WriteTextFileRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case ClientMethodSessionRequestPermission:
		var p RequestPermissionRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case ClientMethodSessionUpdate:
		var p SessionNotification
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case ClientMethodTerminalCreate:
		var p CreateTerminalRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case ClientMethodTerminalKill:
		var p KillTerminalCommandRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case ClientMethodTerminalOutput:
		var p TerminalOutputRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case ClientMethodTerminalRelease:
		var p ReleaseTerminalRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	case ClientMethodTerminalWaitForExit:
		var p WaitForTerminalExitRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.client(ctx, id, p)
	default:
		slog.Warn("unrecognized client method", "method", method)
	}
	return nil
}

func (c *ClientSideConnection) SendResponse(ctx context.Context, idRaw *json.RawMessage, params any) error {
	msg := anyMessage{ID: idRaw}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil
		}
		msg.Result = b
	}

	err := c.conn.send(msg)
	if err != nil {
		return NewInternalError(map[string]any{"error": err.Error()})
	}
	return nil
}

// Send dispatches a client->agent message, inferring the JSON-RPC method from
// the request's Go type. Requests (initialize, session/new, …) are sent with a
// fresh id so their response can be correlated and routed back to the Client
// callback; notifications (session/cancel) are sent without one. The call
// returns once the message is written — responses arrive asynchronously.
func (c *ClientSideConnection) Send(ctx context.Context, request any) error {
	switch request.(type) {
	case InitializeRequest:
		return c.conn.SendRequest(ctx, AgentMethodInitialize, request)
	case NewSessionRequest:
		return c.conn.SendRequest(ctx, AgentMethodSessionNew, request)
	case LoadSessionRequest:
		return c.conn.SendRequest(ctx, AgentMethodSessionLoad, request)
	case PromptRequest:
		return c.conn.SendRequest(ctx, AgentMethodSessionPrompt, request)
	case SetSessionModeRequest:
		return c.conn.SendRequest(ctx, AgentMethodSessionSetMode, request)
	case CancelNotification:
		return c.conn.SendNotification(ctx, AgentMethodSessionCancel, request)
	default:
		return NewInternalError(map[string]any{"error": "acp: unsupported outbound request type"})
	}
}
