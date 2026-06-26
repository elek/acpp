package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// anyMessage is the wire form of a JSON-RPC 2.0 message (request, response or
// notification).
type anyMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RequestError    `json:"error,omitempty"`
}

// MethodHandler handles an inbound request or notification. For notifications
// id is nil and the returned value is ignored. For requests, returning a non-nil
// *RequestError produces a JSON-RPC error response. Returning nil for a request
// means the handler will send the response asynchronously via SendResponse.
type MethodHandler func(ctx context.Context, id *json.RawMessage, method string, params json.RawMessage) *RequestError

// Connection is a JSON-RPC 2.0 connection over line-delimited JSON (stdio).
//
// All inbound messages are processed by a single goroutine in the exact order
// they arrive on the wire. Notifications and inbound requests are dispatched to
// the handler synchronously within that loop, while responses are delivered to
// the goroutine that is waiting on the matching outbound request.
//
// Because the loop is strictly sequential, every notification that the peer
// sends before a response is fully handled before that response is delivered.
// In particular a session/prompt response is observed only after all preceding
// session/update notifications have been processed.
type Connection struct {
	handler MethodHandler

	writeMu sync.Mutex // serializes writes to w
	w       io.Writer
	r       io.Reader

	mu     sync.Mutex // guards pending
	nextID atomic.Uint64
	// pending correlates an outbound request id (its JSON-encoded value) to the
	// method we sent, so that the inbound response — which carries only the id,
	// no method — can be decoded into the right type and dispatched.
	pending map[string]string

	ctx    context.Context
	cancel context.CancelFunc

	logger *slog.Logger
}

// NewConnection creates a connection that writes to peerInput and reads from
// peerOutput, dispatching inbound messages to handler. It starts the receive
// loop immediately.
func NewConnection(handler MethodHandler, peerInput io.Writer, peerOutput io.Reader) *Connection {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Connection{
		handler: handler,
		w:       peerInput,
		r:       peerOutput,
		pending: make(map[string]string),
		ctx:     ctx,
		cancel:  cancel,
	}
	go c.receive()
	return c
}

// SetLogger installs a logger for internal diagnostics.
func (c *Connection) SetLogger(l *slog.Logger) { c.logger = l }

func (c *Connection) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// Done is closed when the receive loop exits (peer disconnected / stream closed).
func (c *Connection) Done() <-chan struct{} { return c.ctx.Done() }

// receive reads and dispatches inbound messages sequentially until the peer
// closes the stream.
func (c *Connection) receive() {
	const (
		initialBufSize = 1 << 20  // 1 MiB
		maxBufSize     = 10 << 20 // 10 MiB
	)
	scanner := bufio.NewScanner(c.r)
	scanner.Buffer(make([]byte, 0, initialBufSize), maxBufSize)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg anyMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.log().Error("acp: failed to parse incoming message", "err", err, "raw", string(line))
			continue
		}

		switch {
		// response to one of our outbound requests: it carries only an id, so we
		// recover the method from the pending map to know how to decode it.
		case msg.ID != nil && msg.Method == "":
			c.dispatchResponse(&msg)
		// inbound request or notification from the peer
		case msg.Method != "":
			c.handleInbound(&msg)
		default:
			c.log().Error("acp: message with neither id nor method", "raw", string(line))
		}
	}

	c.cancel()
}

// dispatchResponse handles a response to an outbound request. It looks up the
// originating method, then hands the result to the handler as if it were a
// notification of that method (id nil — there is nothing to respond to).
func (c *Connection) dispatchResponse(msg *anyMessage) {
	method := c.takePending(string(*msg.ID))
	if method == "" {
		c.log().Warn("acp: response with no matching pending request", "id", string(*msg.ID))
		return
	}
	if msg.Error != nil {
		c.log().Error("acp: error response", "method", method, "error", msg.Error)
		return
	}
	if c.handler != nil {
		if rerr := c.handler(c.ctx, nil, method, msg.Result); rerr != nil {
			c.log().Error("acp: failed to handle response", "method", method, "err", rerr)
		}
	}
}

// takePending removes and returns the method recorded for an outbound request id.
func (c *Connection) takePending(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	method := c.pending[id]
	delete(c.pending, id)
	return method
}

// handleInbound dispatches a notification or request to the handler.
func (c *Connection) handleInbound(req *anyMessage) {
	if c.handler == nil {
		if req.ID != nil {
			_ = c.send(anyMessage{ID: req.ID, Error: NewMethodNotFound(req.Method)})
		}
		return
	}

	rerr := c.handler(c.ctx, req.ID, req.Method, req.Params)

	if req.ID == nil {
		// Notification: no response. Surface unexpected handler errors, but
		// ignore unknown extension notifications (method names starting "_").
		if rerr != nil && !(rerr.Code == -32601 && strings.HasPrefix(req.Method, "_")) {
			c.log().Error("acp: failed to handle notification", "method", req.Method, "err", rerr)
		}
		return
	}

	// On request error, send the error response immediately. On success, the
	// handler is responsible for calling SendResponse asynchronously.
	if rerr != nil {
		_ = c.send(anyMessage{ID: req.ID, Error: rerr})
	}
}

// SendResponse writes a JSON-RPC result response with the given request id.
// no method just ID
func (c *Connection) SendResponse(id *json.RawMessage, result any) error {
	msg := anyMessage{ID: id}
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		msg.Result = b
	}
	return c.send(msg)
}

// SendNotification sends one way notification
// method + param, but no id
func (c *Connection) SendNotification(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return NewInternalError(map[string]any{"error": err.Error()})
	}
	msg := anyMessage{Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		msg.Params = b
	}
	if err := c.send(msg); err != nil {
		return NewInternalError(map[string]any{"error": err.Error()})
	}
	return nil
}

// SendRequest sends a JSON-RPC request (method + params + a fresh id) and
// returns once it is written. It does not wait for the response: the response
// arrives asynchronously on the receive loop and is dispatched to the handler
// via the pending map. Use SendNotification for fire-and-forget messages that
// expect no response.
func (c *Connection) SendRequest(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return NewInternalError(map[string]any{"error": err.Error()})
	}
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	raw := json.RawMessage(idRaw)
	msg := anyMessage{ID: &raw, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return NewInvalidParams(map[string]any{"error": err.Error()})
		}
		msg.Params = b
	}

	c.mu.Lock()
	c.pending[string(idRaw)] = method
	c.mu.Unlock()

	if err := c.send(msg); err != nil {
		c.takePending(string(idRaw))
		return NewInternalError(map[string]any{"error": err.Error()})
	}
	return nil
}

// send writes a single JSON-RPC message followed by a newline.
func (c *Connection) send(msg anyMessage) error {
	msg.JSONRPC = "2.0"
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(b)
	return err
}
