// Package router owns conversation lifecycle and is the central event hub. A
// conversation is the durable unit (its ID is what channels and the database
// key off); it is currently backed 1:1 by an ACP session running in a
// subprocess. The router assembles those pieces — it owns the dedicated
// process.Manager, creates sessions through it, pumps each session's raw ACP
// update stream to subscribed listeners, and accepts prompts via Send.
//
// See docs/plans/2026-06-21-router-refactor-design.md.
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/process"
	"github.com/elek/acpp/types"
	"github.com/google/uuid"
)

// Router owns the process manager and the set of live conversations. It is safe
// for concurrent use: the receive loop of every ACP connection calls back into
// Receive while callers Create, Send and Close from other goroutines.
type Router struct {
	procs  *process.Manager
	ctx    context.Context
	cancel context.CancelFunc

	// shutdown, if set via OnShutdown, is invoked by the /exit command to bring
	// the whole application down (typically the main context's cancel func).
	shutdown context.CancelFunc

	// sessions is keyed by ConversationID — a random UUID minted at Create that
	// is stable for the whole conversation lifetime. The ACP SessionID is filled
	// in asynchronously once the initialize/new-session handshake completes, so it
	// cannot serve as a key.
	mu          sync.RWMutex
	sessions    map[string]*SessionState
	subscribers []Subscriber
}

// New creates a Router with its own dedicated process manager.
func New() *Router {
	ctx, cancel := context.WithCancel(context.Background())
	return &Router{
		procs:    process.NewManager(),
		ctx:      ctx,
		cancel:   cancel,
		sessions: make(map[string]*SessionState),
	}
}

type SessionState struct {
	// meta is the authoritative conversation metadata. Its ConversationID is
	// stable; its SessionID is empty until the handshake fills it. Guarded by
	// Router.mu.
	meta        types.ConversationMeta
	sessionData acp.NewSessionResponse
	acpInit     acp.InitializeResponse
	connection  *acp.ClientSideConnection
	// proc is the subprocess backing this conversation, retained so a single
	// conversation can be closed without tearing down the whole router.
	proc *process.Process
	// opts are the options the session was created with, retained so Restart can
	// re-issue NewSession against the same process with the same cwd.
	opts types.SessionOpts
	// ready is closed once the current session's SessionID is populated (by the
	// session/new response). Restart replaces it to wait for the next session.
	// Guarded by Router.mu; nil once already consumed.
	ready chan struct{}
}

// OnShutdown registers the cancel function the /exit command invokes to shut the
// application down. Call it once during wiring with the main context's cancel.
func (r *Router) OnShutdown(cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shutdown = cancel
}

// Subscribe registers a listener that receives every conversation's raw ACP
// updates. Subscribe before creating conversations so no early updates are
// missed.
func (r *Router) Subscribe(s Subscriber) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers = append(r.subscribers, s)
}

// Create starts a new conversation with the given session options and returns
// its ConversationMeta immediately. The returned meta has a stable
// ConversationID but an empty SessionID: the ACP initialize/new-session
// handshake runs asynchronously and fills in the SessionID later (callers that
// need it can block via WaitReady). The subprocess is bound to the router's
// lifetime (not ctx).
func (r *Router) Create(ctx context.Context, opts types.SessionOpts) (types.ConversationMeta, error) {
	ps, err := r.procs.Start(r.ctx, process.Spec{
		Agent:   opts.Agent,
		Cwd:     opts.CWD,
		Env:     opts.Env,
		Sandbox: opts.Sandbox,
	})
	if err != nil {
		return types.ConversationMeta{}, err
	}

	convID := uuid.NewString()
	meta := types.ConversationMeta{
		ProjectID:      opts.ProjectID,
		ProcessPID:     ps.PID(),
		ConversationID: convID,
	}
	state := &SessionState{
		meta:  meta,
		proc:  ps,
		opts:  opts,
		ready: make(chan struct{}),
	}

	r.mu.Lock()
	r.sessions[convID] = state
	r.mu.Unlock()

	// Every inbound message is tagged with this conversation's stable id; the
	// receive loop never needs to learn a new key even after the session id is
	// assigned, because the map is keyed by the UUID, not the meta.
	handler := func(ctx context.Context, rid *json.RawMessage, msg any) {
		r.onMessage(ctx, convID, rid, msg)
	}

	connection := acp.NewClientSideConnection(handler, ps.Stdin, ps.Stdout)

	r.mu.Lock()
	state.connection = connection
	r.mu.Unlock()

	// Kick off the handshake. The agent only emits the initialize response after
	// receiving this, so state.connection is guaranteed set by the time onMessage
	// needs it to fire session/new.
	err = connection.Send(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapability{ReadTextFile: true, WriteTextFile: true},
		},
	})
	if err != nil {
		r.mu.Lock()
		delete(r.sessions, convID)
		r.mu.Unlock()
		ps.Close()
		return types.ConversationMeta{}, err
	}

	return meta, nil
}

// onMessage receives every inbound ACP message for a conversation. It drives the
// async handshake — initialize response triggers session/new; the session/new
// response fills in the SessionID and unblocks WaitReady — and fans everything
// else out to subscribers tagged with the conversation's current meta.
func (r *Router) onMessage(ctx context.Context, convID string, rid *json.RawMessage, msg any) {
	switch m := msg.(type) {
	case acp.InitializeResponse:
		r.mu.Lock()
		state := r.sessions[convID]
		var conn *acp.ClientSideConnection
		var cwd string
		if state != nil {
			state.acpInit = m
			conn = state.connection
			cwd = state.opts.CWD
		}
		r.mu.Unlock()
		if conn == nil {
			return
		}
		if err := conn.Send(ctx, acp.NewSessionRequest{
			Cwd:        cwd,
			McpServers: []acp.McpServer{},
		}); err != nil {
			slog.Error("router: send session/new", "conversation_id", convID, "error", err)
		}
		return
	case acp.NewSessionResponse:
		r.mu.Lock()
		state := r.sessions[convID]
		var ready chan struct{}
		var meta types.ConversationMeta
		if state != nil {
			state.sessionData = m
			state.meta.SessionID = m.SessionId
			ready, state.ready = state.ready, nil
			meta = state.meta
		}
		r.mu.Unlock()
		if state == nil {
			return
		}
		// Fan out the raw session/new response before closing ready: subscribers
		// run synchronously on this goroutine, so a persister has written the
		// session row by the time a caller resumes from WaitReady (and before any
		// update logs arrive). The meta already carries the freshly assigned
		// SessionID; subscribers needing the creation options fetch them via
		// Router.Opts.
		r.Receive(ctx, rid, meta, m)
		if ready != nil {
			close(ready)
		}
		return
	default:
		r.mu.RLock()
		state := r.sessions[convID]
		var meta types.ConversationMeta
		if state != nil {
			meta = state.meta
		}
		r.mu.RUnlock()
		r.Receive(ctx, rid, meta, msg)
	}
}

// WaitReady blocks until the conversation's SessionID has been assigned by the
// handshake (or ctx is cancelled). It returns the up-to-date meta.
func (r *Router) WaitReady(ctx context.Context, id types.ConversationMeta) (types.ConversationMeta, error) {
	r.mu.RLock()
	state, ok := r.sessions[id.ConversationID]
	var ready chan struct{}
	if ok {
		ready = state.ready
	}
	r.mu.RUnlock()
	if !ok {
		return types.ConversationMeta{}, fmt.Errorf("router: unknown conversation %v", id)
	}
	if ready != nil {
		select {
		case <-ready:
		case <-ctx.Done():
			return types.ConversationMeta{}, ctx.Err()
		case <-r.ctx.Done():
			return types.ConversationMeta{}, fmt.Errorf("router: shutting down")
		}
	}
	r.mu.RLock()
	meta := state.meta
	r.mu.RUnlock()
	return meta, nil
}

// Opts returns the options the conversation was created with, looked up by its
// stable ConversationID. It is the synchronized way for a subscriber to recover
// the creation options when handling the raw session/new response (which carries
// only the protocol session id). The bool is false for an unknown conversation.
func (r *Router) Opts(convID string) (types.SessionOpts, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if state, ok := r.sessions[convID]; ok {
		return state.opts, true
	}
	return types.SessionOpts{}, false
}

// Active reports whether a conversation with the given ConversationID is still
// live (created and not yet closed).
func (r *Router) Active(conversationID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.sessions[conversationID]
	return ok
}

// Restart starts a fresh ACP session on the conversation's existing process,
// discarding the prior conversation context. The process (and PID) and the
// stable ConversationID are reused; only the SessionID changes. A
// types.ConversationReplaced event is emitted once the new session is ready so
// channels can react (re-key by session id, reset buffers, …). Returns the
// updated meta.
func (r *Router) Restart(ctx context.Context, id types.ConversationMeta) (types.ConversationMeta, error) {
	r.mu.Lock()
	state, ok := r.sessions[id.ConversationID]
	var old types.ConversationMeta
	if ok {
		old = state.meta
		state.meta.SessionID = ""
		state.ready = make(chan struct{})
	}
	r.mu.Unlock()
	if !ok {
		return types.ConversationMeta{}, fmt.Errorf("router: unknown conversation %v", id)
	}

	if err := state.connection.Send(ctx, acp.NewSessionRequest{
		Cwd:        state.opts.CWD,
		McpServers: []acp.McpServer{},
	}); err != nil {
		return types.ConversationMeta{}, fmt.Errorf("router: restart conversation %v: %w", id, err)
	}

	newMeta, err := r.WaitReady(ctx, id)
	if err != nil {
		return types.ConversationMeta{}, fmt.Errorf("router: restart conversation %v: %w", id, err)
	}

	r.Receive(ctx, nil, newMeta, types.ConversationReplaced{Old: old, New: newMeta})
	return newMeta, nil
}

// Respond sends a result for an inbound agent request (e.g. a permission
// request) on the conversation identified by id, echoing the agent's request id.
func (r *Router) Respond(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) error {
	r.mu.RLock()
	state, ok := r.sessions[id.ConversationID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("router: unknown conversation %v", id)
	}
	return state.connection.SendResponse(ctx, rid, msg)
}

// Send dispatches a client->agent message (request or notification) on the
// conversation identified by id; the method is inferred from msg's type. The
// same raw message is first fanned out to subscribers via Receive (tagged with
// the conversation's authoritative meta) so they can persist or echo it — a
// PromptRequest, for instance, renders as the user's submitted prompt.
func (r *Router) Send(ctx context.Context, id types.ConversationMeta, msg any) error {
	r.mu.RLock()
	state, ok := r.sessions[id.ConversationID]
	var meta types.ConversationMeta
	if ok {
		meta = state.meta
	}
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("router: unknown conversation %v", id)
	}
	r.Receive(ctx, nil, meta, msg)
	return state.connection.Send(ctx, msg)
}

// CloseConversation shuts down the subprocess backing a single conversation and
// removes it from the router, leaving every other conversation running. It is a
// no-op for an unknown id.
func (r *Router) CloseConversation(id types.ConversationMeta) {
	r.mu.Lock()
	state, ok := r.sessions[id.ConversationID]
	if ok {
		delete(r.sessions, id.ConversationID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	// Let subscribers finalize per-conversation state before the process dies.
	r.Receive(r.ctx, nil, state.meta, types.ConversationClosed{Meta: state.meta})
	if state.proc != nil {
		state.proc.Close()
	}
}

// Close shuts every conversation's subprocess down gracefully and releases the
// router's resources. It is safe to call multiple times.
func (r *Router) Close() {
	// Graceful first (stdin EOF -> wait -> SIGTERM per process), then cancel the
	// router context as a backstop for anything still bound to it.
	r.procs.CloseAll()
	r.mu.Lock()
	metas := make([]types.ConversationMeta, 0, len(r.sessions))
	for _, state := range r.sessions {
		metas = append(metas, state.meta)
	}
	r.sessions = make(map[string]*SessionState)
	r.mu.Unlock()
	// Finalize each conversation for subscribers before tearing the context down.
	for _, meta := range metas {
		r.Receive(r.ctx, nil, meta, types.ConversationClosed{Meta: meta})
	}
	r.cancel()
}

// Receive fans a single conversation update out to all current subscribers. The
// subscriber slice is snapshotted under the lock and invoked without it, so a
// subscriber may safely call back into the router.
func (r *Router) Receive(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	r.mu.RLock()
	subs := make([]Subscriber, len(r.subscribers))
	copy(subs, r.subscribers)
	r.mu.RUnlock()
	for _, s := range subs {
		s(ctx, rid, id, msg)
	}
}

// Subscriber is invoked for every raw ACP update flowing through the router.
type Subscriber func(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any)
