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
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/hook"
	"github.com/elek/acpp/process"
	"github.com/elek/acpp/sandbox"
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

	// cfg supplies defaults used when resolving a conversation's .acpp.yaml at
	// Create time (agent, sandbox). Never nil — New defaults it to an empty config.
	cfg *config.Config

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

// Option configures a Router at construction.
type Option func(*Router)

// WithConfig supplies the global config used to resolve per-conversation
// .acpp.yaml defaults (agent, sandbox) at Create time. Without it the router
// resolves against an empty config.
func WithConfig(cfg *config.Config) Option {
	return func(r *Router) {
		if cfg != nil {
			r.cfg = cfg
		}
	}
}

// New creates a Router with its own dedicated process manager.
func New(opts ...Option) *Router {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Router{
		procs:    process.NewManager(),
		ctx:      ctx,
		cancel:   cancel,
		cfg:      &config.Config{},
		sessions: make(map[string]*SessionState),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
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
	// handshakeErr records an errored response to a handshake request (initialize
	// / session/new); it closes ready so WaitReady returns this error instead of
	// blocking for a SessionID that will never arrive. Guarded by Router.mu.
	handshakeErr error
	// availableCommands holds the latest set of commands the agent advertised via
	// an available_commands_update notification (the only place the agent exposes
	// them — they are absent from the initialize/session-new responses). Used by
	// /help; replaced on each update and cleared on Restart. Guarded by Router.mu.
	availableCommands []acp.AvailableCommand
	// hooks are this conversation's message-transform hooks, instantiated from the
	// project's .acpp.yaml at Create. Each conversation has its own instances so
	// hook state (e.g. commit's hasCommitted) is independent. Immutable after
	// Create, so safe to read without the lock.
	hooks []hook.Hook
	// baseDir is the working directory originally requested for this session,
	// captured before any session hook rewrote opts.CWD. It is what
	// runningSessionsForDir counts against, so a worktree'd session still counts
	// toward its origin repo's contention. Immutable after Create.
	baseDir string
	// cleanups are session-hook teardown funcs, run exactly once (guarded by
	// cleanupOnce) when the conversation is finalized. Immutable after Create.
	cleanups    []func()
	cleanupOnce sync.Once
}

// runCleanups runs this session's hook teardown funcs exactly once, in reverse
// order. Safe to call from any finalize path (deliberate close, subprocess-exit
// watcher, or a failed Create).
func (s *SessionState) runCleanups() {
	s.cleanupOnce.Do(func() { runCleanups(s.cleanups) })
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
	hooks, sbType, profiles, err := r.resolveProject(&opts)
	if err != nil {
		return types.ConversationMeta{}, err
	}

	// The conversation id is minted before the session hooks run so a hook can
	// name per-conversation resources after it (the worktree hook names the
	// worktree branch/dir by convID).
	convID := uuid.NewString()
	baseDir := opts.CWD

	// Session hooks run before the sandbox is resolved and the subprocess starts,
	// so a hook may redirect opts.CWD and add opts.RWBinds. Each returns a cleanup
	// func run when the conversation is finalized. On error, unwind the cleanups
	// already collected and abort before starting anything.
	var cleanups []func()
	// Harness notices queued by session hooks (via sc.Notify) are emitted after the
	// conversation is announced so they land as its first messages, in order.
	var notices []string
	sc := hook.SessionContext{
		Meta:                  types.ConversationMeta{ProjectID: opts.ProjectID, ConversationID: convID},
		ConversationID:        convID,
		BaseDir:               baseDir,
		RunningSessionsForDir: r.runningSessionsForDir,
		Notify:                func(text string) { notices = append(notices, text) },
	}
	for _, h := range hooks {
		sh, ok := h.(hook.SessionHook)
		if !ok {
			continue
		}
		cleanup, err := sh.SetupSession(sc, &opts)
		if err != nil {
			runCleanups(cleanups)
			return types.ConversationMeta{}, err
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}

	// Build the sandbox now that opts.CWD / opts.RWBinds reflect any redirection.
	if err := r.resolveSandbox(&opts, sbType, profiles); err != nil {
		runCleanups(cleanups)
		return types.ConversationMeta{}, err
	}

	ps, err := r.procs.Start(r.ctx, process.Spec{
		Agent:   opts.Agent,
		Cwd:     opts.CWD,
		Env:     opts.Env,
		Sandbox: opts.Sandbox,
	})
	if err != nil {
		runCleanups(cleanups)
		return types.ConversationMeta{}, err
	}

	meta := types.ConversationMeta{
		ProjectID:      opts.ProjectID,
		ProcessPID:     ps.PID(),
		ConversationID: convID,
	}
	state := &SessionState{
		meta:     meta,
		proc:     ps,
		opts:     opts,
		ready:    make(chan struct{}),
		hooks:    hooks,
		baseDir:  baseDir,
		cleanups: cleanups,
	}

	r.mu.Lock()
	r.sessions[convID] = state
	r.mu.Unlock()

	// Announce the conversation before the handshake runs: subscribers fire
	// synchronously on this goroutine, so the persister has written the session
	// row (keyed by the stable ConversationID) before any ACP update can arrive
	// and before Create returns.
	r.Receive(ctx, nil, meta, types.ConversationCreated{Meta: meta})

	// Emit any harness notices a session hook queued (e.g. the worktree hook
	// naming the worktree it created) as the conversation's first messages: after
	// ConversationCreated so they persist against the row, before any agent output.
	// Tagged _meta.acpp.type=notice so surfaces render them as harness notices
	// rather than agent output.
	for _, text := range notices {
		r.Receive(ctx, nil, meta, acp.SessionNotification{
			Update: acp.SessionUpdate{
				AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Meta:    map[string]any{"acpp": map[string]any{"type": "notice"}},
					Content: acp.TextBlock(text),
				},
			},
		})
	}

	// Every inbound message is tagged with this conversation's stable id; the
	// receive loop never needs to learn a new key even after the session id is
	// assigned, because the map is keyed by the UUID, not the meta.
	handler := func(ctx context.Context, rid *json.RawMessage, msg any) {
		r.onMessage(ctx, state, rid, msg)
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
		state.runCleanups()
		return types.ConversationMeta{}, err
	}

	// Finalize the conversation if the subprocess ever exits without a deliberate
	// close, so a lost turn completion can never wedge the conversation (or a
	// scheduled job) forever.
	go r.watchProcess(state, ps)

	return meta, nil
}

// CreateError records a stillborn conversation: one that could not start an ACP
// session at all (e.g. its working directory could not be resolved). It mints a
// conversation id, announces the conversation so the persister writes its row,
// fans a single agent_message_chunk carrying the failure text and a
// _meta.acpp.type=error marker (so surfaces can render it as a harness error
// rather than agent output), then closes it as errored. No subprocess is started,
// so its ACP SessionID stays empty. Returns the conversation meta.
func (r *Router) CreateError(ctx context.Context, opts types.SessionOpts, errMsg string) types.ConversationMeta {
	convID := uuid.NewString()
	meta := types.ConversationMeta{
		ProjectID:      opts.ProjectID,
		ConversationID: convID,
	}
	// Register minimal state (no process, no connection) so Router.Opts can supply
	// the creation options when the persister writes the row on ConversationCreated.
	r.mu.Lock()
	r.sessions[convID] = &SessionState{meta: meta, opts: opts}
	r.mu.Unlock()

	// Subscribers run synchronously: the row is written, then the error message is
	// logged against it, then the conversation is closed as errored — in order.
	r.Receive(ctx, nil, meta, types.ConversationCreated{Meta: meta})
	r.Receive(ctx, nil, meta, acp.SessionNotification{
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Meta:    map[string]any{"acpp": map[string]any{"type": "error"}},
				Content: acp.TextBlock(errMsg),
			},
		},
	})
	r.closeConversation(meta, errMsg)
	return meta
}

// resolveProject loads the conversation's .acpp.yaml (from opts.CWD) and folds it
// over opts: the project file's agent and sandbox win over the caller's, which in
// turn win over the global config defaults. It resolves opts.Agent and returns
// the project's hooks plus the resolved sandbox type/profiles. The sandbox itself
// is NOT built here — Router.Create builds it via resolveSandbox after the
// session hooks have run, so a hook that rewrites opts.CWD (e.g. the worktree
// hook) is reflected in the bind mounts. Centralizing this here means .acpp.yaml
// is honored by every channel that creates a conversation. A missing file is not
// an error.
func (r *Router) resolveProject(opts *types.SessionOpts) (hooks []hook.Hook, sbType, profiles string, err error) {
	pc, err := config.LoadProject(opts.CWD)
	if err != nil {
		return nil, "", "", err
	}

	// Agent: project file > caller > config default, then resolved against AgentPath.
	agent := opts.Agent
	if pc.Agent != "" {
		agent = pc.Agent
	}
	if agent == "" {
		agent = r.cfg.Defaults.Agent
	}
	opts.Agent = r.cfg.ResolveAgent(agent)

	// Sandbox settings: project file > caller's type/profiles > config default.
	// Only meaningful when the caller has not pre-built a Sandbox (resolveSandbox
	// honors that). Channels pass the sandbox as strings and leave Sandbox nil so
	// this is the single place .acpp.yaml is folded in.
	sbType, profiles = opts.SandboxType, opts.SandboxProfiles
	if pc.Sandbox.Name != "" {
		sbType, profiles = pc.Sandbox.Name, pc.Sandbox.Profiles
	} else if sbType == "" {
		sbType = r.cfg.Defaults.Sandbox
	}

	// Hooks: global config hooks run first, then the project's own. Building both
	// in one call means an unknown type in either source fails loudly here.
	hooks, err = hook.Build(append(append([]config.HookConfig{}, r.cfg.Hooks...), pc.Hooks...))
	if err != nil {
		return nil, "", "", err
	}
	return hooks, sbType, profiles, nil
}

// resolveSandbox builds opts.Sandbox from the resolved sandbox type/profiles,
// unless the caller already supplied a Sandbox. It is called from Create AFTER
// the session hooks have run, so opts.CWD / opts.ROBinds / opts.RWBinds reflect
// any redirection. A pre-built Sandbox opts out (its profiles are the caller's).
func (r *Router) resolveSandbox(opts *types.SessionOpts, sbType, profiles string) error {
	if opts.Sandbox != nil || sbType == "" {
		return nil
	}
	sb, err := sandbox.ResolveSandbox(sbType, profiles, opts.CWD, opts.ROBinds, opts.RWBinds)
	if err != nil {
		return fmt.Errorf("router: resolving sandbox %q: %w", sbType, err)
	}
	opts.Sandbox = sb
	opts.SandboxType = sbType
	opts.SandboxProfiles = profiles
	return nil
}

// runningSessionsForDir reports how many live conversations were created with the
// given base directory (the directory originally requested, before any session
// hook rewrote opts.CWD). A conversation is removed from the map on close, so map
// membership is the liveness signal. Used by the worktree hook to detect a repo
// that already has a running session.
func (r *Router) runningSessionsForDir(dir string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.sessions {
		if s.baseDir == dir {
			n++
		}
	}
	return n
}

// runCleanups invokes cleanup funcs in reverse of the order they were collected,
// skipping nils. Used for session-hook teardown.
func runCleanups(cleanups []func()) {
	for i := len(cleanups) - 1; i >= 0; i-- {
		if cleanups[i] != nil {
			cleanups[i]()
		}
	}
}

// onMessage receives every inbound ACP message for a conversation. It drives the
// async handshake — initialize response triggers session/new; the session/new
// response fills in the SessionID and unblocks WaitReady — and fans everything
// else out to subscribers tagged with the conversation's current meta.
func (r *Router) onMessage(ctx context.Context, state *SessionState, rid *json.RawMessage, msg any) {
	// state is captured by the receive-loop handler at Create, so it stays valid
	// even after Restart re-keys the session map under a fresh ConversationID —
	// looking the state up by a captured key would break delivery post-/clear.
	switch m := msg.(type) {
	case acp.InitializeResponse:
		r.mu.Lock()
		state.acpInit = m
		conn := state.connection
		cwd := state.opts.CWD
		cid := state.meta.ConversationID
		r.mu.Unlock()
		if conn == nil {
			return
		}
		if err := conn.Send(ctx, acp.NewSessionRequest{
			Cwd:        cwd,
			McpServers: []acp.McpServer{},
		}); err != nil {
			slog.Error("router: send session/new", "conversation_id", cid, "error", err)
		}
		return
	case acp.NewSessionResponse:
		r.mu.Lock()
		state.sessionData = m
		state.meta.SessionID = m.SessionId
		ready := state.ready
		state.ready = nil
		meta := state.meta
		r.mu.Unlock()
		// Fan out the raw session/new response before closing ready: subscribers
		// run synchronously on this goroutine, so a persister has recorded the
		// ACP session id by the time a caller resumes from WaitReady. The meta
		// already carries the freshly assigned SessionID; subscribers needing the
		// creation options fetch them via Router.Opts.
		r.deliver(ctx, state, rid, meta, m)
		if ready != nil {
			close(ready)
		}
		return
	case acp.ResponseError:
		// An outbound request failed. A failure during the handshake (before the
		// SessionID is assigned) means no session/new response will ever arrive, so
		// unblock WaitReady with the error rather than leaving it hung. A
		// post-handshake failure (a prompt error) leaves ready untouched and just
		// fans out below, where subscribers end the turn.
		r.mu.Lock()
		if state.ready != nil {
			state.handshakeErr = m
			close(state.ready)
			state.ready = nil
		}
		meta := state.meta
		r.mu.Unlock()
		r.deliver(ctx, state, rid, meta, msg)
		return
	default:
		r.mu.Lock()
		// Capture the agent's advertised commands so /help can list them; this
		// notification is the only place the agent exposes them.
		if n, ok := msg.(acp.SessionNotification); ok && n.Update.AvailableCommandsUpdate != nil {
			state.availableCommands = n.Update.AvailableCommandsUpdate.AvailableCommands
		}
		meta := state.meta
		r.mu.Unlock()
		r.deliver(ctx, state, rid, meta, msg)
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
	herr := state.handshakeErr
	r.mu.RUnlock()
	if herr != nil {
		return meta, herr
	}
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

// Init returns the agent's initialize response for a conversation, looked up by
// its stable ConversationID. Unlike most updates the initialize response is not
// fanned out to subscribers (it is consumed internally to drive session/new), so
// this accessor is the synchronized way to inspect the negotiated agent
// capabilities after WaitReady. The bool is false for an unknown conversation.
func (r *Router) Init(convID string) (acp.InitializeResponse, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if state, ok := r.sessions[convID]; ok {
		return state.acpInit, true
	}
	return acp.InitializeResponse{}, false
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
	newConvID := uuid.NewString()
	r.mu.Lock()
	state, ok := r.sessions[id.ConversationID]
	var old, fresh types.ConversationMeta
	if ok {
		old = state.meta
		// One conversation per session: rather than reuse the ConversationID (which
		// would fold the restarted session's history into the old row), roll it and
		// re-key the map. The subprocess is reused; only the conversation identity
		// changes. Delivery survives the re-key because the receive-loop handler and
		// watchProcess hold the state pointer, not the map key.
		delete(r.sessions, old.ConversationID)
		state.meta.ConversationID = newConvID
		state.meta.SessionID = ""
		state.ready = make(chan struct{})
		// Discard the prior session's advertised commands; the fresh session will
		// re-advertise its own via available_commands_update.
		state.availableCommands = nil
		r.sessions[newConvID] = state
		fresh = state.meta
	}
	r.mu.Unlock()
	if !ok {
		return types.ConversationMeta{}, fmt.Errorf("router: unknown conversation %v", id)
	}

	// Announce the fresh conversation (new id, same process) before the handshake,
	// mirroring Create, so the persister writes its row first.
	r.Receive(ctx, nil, fresh, types.ConversationCreated{Meta: fresh})

	if err := state.connection.Send(ctx, acp.NewSessionRequest{
		Cwd:        state.opts.CWD,
		McpServers: []acp.McpServer{},
	}); err != nil {
		return types.ConversationMeta{}, fmt.Errorf("router: restart conversation %v: %w", id, err)
	}

	newMeta, err := r.WaitReady(ctx, fresh)
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
	return r.dispatch(ctx, state, meta, msg, true)
}

// dispatch fans a client->agent message out to subscribers and sends it to the
// agent. When applyOutgoing is set, each hook's Outgoing runs first and may
// rewrite the message or drop it (returning nil). Trigger-injected prompts call
// dispatch with applyOutgoing=false so a hook cannot re-trigger itself in a loop.
func (r *Router) dispatch(ctx context.Context, state *SessionState, meta types.ConversationMeta, msg any, applyOutgoing bool) error {
	if applyOutgoing {
		hc := hook.HookContext{
			Meta: meta,
			CWD:  state.opts.CWD,
			Trigger: func(prompt string) error {
				return r.triggerPrompt(ctx, state, prompt)
			},
		}
		for _, h := range state.hooks {
			msg = h.Outgoing(hc, msg)
			if msg == nil {
				return nil
			}
		}
	}
	r.Receive(ctx, nil, meta, msg)
	return state.connection.Send(ctx, msg)
}

// triggerPrompt submits a hook-injected follow-up prompt through the dispatch
// pipeline (fan-out + send) using the conversation's current SessionID, bypassing
// Outgoing to avoid re-entrancy.
func (r *Router) triggerPrompt(ctx context.Context, state *SessionState, prompt string) error {
	r.mu.RLock()
	meta := state.meta
	r.mu.RUnlock()
	req := acp.PromptRequest{
		SessionId: meta.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
	}
	return r.dispatch(ctx, state, meta, req, false)
}

// deliver runs an agent->subscriber message through each hook's Incoming, fans the
// (possibly rewritten) result out to subscribers, then flushes any follow-up
// prompts the hooks requested via Trigger. Triggers are deferred until after the
// current message is delivered so subscribers see strictly in-order events:
// [turn updates] -> [response] -> [follow-up prompt].
func (r *Router) deliver(ctx context.Context, state *SessionState, rid *json.RawMessage, meta types.ConversationMeta, msg any) {
	if state == nil || len(state.hooks) == 0 {
		r.Receive(ctx, rid, meta, msg)
		return
	}
	var pending []string
	hc := hook.HookContext{
		Meta: meta,
		CWD:  state.opts.CWD,
		Trigger: func(prompt string) error {
			pending = append(pending, prompt)
			return nil
		},
	}
	for _, h := range state.hooks {
		msg = h.Incoming(hc, msg)
		if msg == nil {
			break
		}
	}
	if msg != nil {
		r.Receive(ctx, rid, meta, msg)
	}
	for _, prompt := range pending {
		if err := r.triggerPrompt(ctx, state, prompt); err != nil {
			slog.Error("router: hook trigger failed", "conversation_id", meta.ConversationID, "error", err)
		}
	}
}

// CloseConversation shuts down the subprocess backing a single conversation and
// removes it from the router, leaving every other conversation running. It is a
// no-op for an unknown id.
func (r *Router) CloseConversation(id types.ConversationMeta) {
	r.closeConversation(id, "")
}

// closeConversation removes a conversation and fans a ConversationClosed to
// subscribers so they can finalize per-conversation state before the process
// dies. errMsg, when non-empty, marks the close as abnormal (the agent
// subprocess exited before completing its turn) so subscribers finalize
// accordingly — the persister records the session as errored rather than
// complete, and the scheduler releases the job that started it. The guarded
// delete makes it safe for concurrent callers (a deliberate close racing the
// subprocess-exit watcher): only the caller that removes the conversation fans
// the event. It is a no-op for an unknown id.
func (r *Router) closeConversation(id types.ConversationMeta, errMsg string) {
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
	r.Receive(r.ctx, nil, state.meta, types.ConversationClosed{Meta: state.meta, Err: errMsg})
	if state.proc != nil {
		state.proc.Close()
	}
	// Session-hook teardown runs after the agent process is gone so nothing in the
	// worktree is still held open when it is removed.
	state.runCleanups()
}

// watchProcess finalizes a conversation whose agent subprocess exits before the
// conversation is closed deliberately. A normal close (CloseConversation/Close)
// removes the conversation from the map first, so by the time the process dies
// the lookup here finds nothing and this is a no-op. An unexpected exit — a
// crash, an OOM-kill, or the agent quitting without ever sending its
// session/prompt response — otherwise leaves the conversation registered
// forever: Active stays true, its session row is stuck 'pending', and a
// scheduled job that started it never releases (every later tick is skipped as
// "previous run still active"). Fanning an errored ConversationClosed lets every
// subscriber finalize.
func (r *Router) watchProcess(state *SessionState, ps *process.Process) {
	select {
	case <-ps.Done():
	case <-r.ctx.Done():
		return
	}
	// Read the current meta from state (not a captured key): Restart may have
	// rolled the ConversationID while reusing this same process. Confirm the
	// conversation is still registered under its current id before finalizing, so
	// a deliberate close that already removed it stays a no-op.
	r.mu.RLock()
	meta := state.meta
	_, ok := r.sessions[meta.ConversationID]
	r.mu.RUnlock()
	if !ok {
		return
	}
	slog.Warn("agent subprocess exited before its conversation was closed; finalizing",
		"conversation_id", meta.ConversationID, "pid", meta.ProcessPID)
	r.closeConversation(meta, "agent subprocess exited before completing the turn")
}

// Close shuts every conversation's subprocess down gracefully and releases the
// router's resources. It is safe to call multiple times.
func (r *Router) Close() {
	// Graceful first (stdin EOF -> wait -> SIGTERM per process), then cancel the
	// router context as a backstop for anything still bound to it.
	r.procs.CloseAll()
	r.mu.RLock()
	metas := make([]types.ConversationMeta, 0, len(r.sessions))
	for _, state := range r.sessions {
		metas = append(metas, state.meta)
	}
	r.mu.RUnlock()
	// Finalize each conversation through the guarded path so subscribers see
	// exactly one ConversationClosed per conversation even though CloseAll above
	// has woken every subprocess-exit watcher, which races us to finalize.
	for _, meta := range metas {
		r.closeConversation(meta, "")
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
