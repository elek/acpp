package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/hook"
	"github.com/elek/acpp/process"
	"github.com/elek/acpp/types"
)

// descriptor is what a remote host keeps next to an agent process so that a
// restarted server can take the conversation back (Adopt): everything Create
// knew that is not in the database or cannot be rebuilt from the project's
// config. It travels as opaque JSON; only the router reads it.
type descriptor struct {
	ConversationID string `json:"conversation_id"`
	// StreamID is the id the host knows the process by: the ConversationID the
	// conversation started with (a /clear rolls ConversationID, not this).
	StreamID        string   `json:"stream_id,omitempty"`
	SessionID       string   `json:"session_id,omitempty"`
	ProjectID       string   `json:"project_id,omitempty"`
	Agent           string   `json:"agent,omitempty"`
	CWD             string   `json:"cwd,omitempty"`
	BaseDir         string   `json:"base_dir,omitempty"`
	Env             []string `json:"env,omitempty"`
	Source          string   `json:"source,omitempty"`
	SandboxType     string   `json:"sandbox_type,omitempty"`
	SandboxProfiles string   `json:"sandbox_profiles,omitempty"`
	ROBinds         []string `json:"ro_binds,omitempty"`
	RWBinds         []string `json:"rw_binds,omitempty"`
	Location        string   `json:"location,omitempty"`
	// Hooks holds each resumable hook's state, by its position in the
	// conversation's hook list. Type guards against a hook list that changed in
	// the meantime: state is only handed to a hook of the same type.
	Hooks []hookState `json:"hooks,omitempty"`
}

type hookState struct {
	Index int               `json:"index"`
	Type  string            `json:"type"`
	State map[string]string `json:"state"`
}

func newDescriptor(convID, streamID string, opts types.SessionOpts, baseDir string, hooks []hook.Hook) descriptor {
	d := descriptor{
		ConversationID:  convID,
		StreamID:        streamID,
		SessionID:       string(opts.ResumeSessionID),
		ProjectID:       opts.ProjectID,
		Agent:           opts.Agent,
		CWD:             opts.CWD,
		BaseDir:         baseDir,
		Env:             opts.Env,
		Source:          opts.Source,
		SandboxType:     opts.SandboxType,
		SandboxProfiles: opts.SandboxProfiles,
		ROBinds:         opts.ROBinds,
		RWBinds:         opts.RWBinds,
		Location:        opts.Location,
	}
	for i, h := range hooks {
		if rh, ok := h.(hook.Resumable); ok {
			if st := rh.SessionState(); st != nil {
				d.Hooks = append(d.Hooks, hookState{Index: i, Type: fmt.Sprintf("%T", h), State: st})
			}
		}
	}
	return d
}

func (d descriptor) marshal() []byte {
	b, _ := json.Marshal(d)
	return b
}

func (d descriptor) opts() types.SessionOpts {
	return types.SessionOpts{
		ProjectID:       d.ProjectID,
		Agent:           d.Agent,
		CWD:             d.CWD,
		Env:             d.Env,
		Source:          d.Source,
		SandboxType:     d.SandboxType,
		SandboxProfiles: d.SandboxProfiles,
		ROBinds:         d.ROBinds,
		RWBinds:         d.RWBinds,
		Location:        d.Location,
	}
}

// pushDescriptor refreshes the descriptor a remote host keeps for the
// conversation, once the handshake (or a /clear) has assigned the ACP session id
// it needs to be adoptable. A no-op for local processes.
func (r *Router) pushDescriptor(state *SessionState) {
	d, ok := state.proc.(process.Detachable)
	if !ok {
		return
	}
	r.mu.RLock()
	desc := newDescriptor(state.meta.ConversationID, state.streamID, state.opts, state.baseDir, state.hooks)
	desc.SessionID = string(state.meta.SessionID)
	r.mu.RUnlock()
	if err := d.UpdateDescriptor(desc.marshal()); err != nil {
		slog.Warn("router: could not update the remote conversation descriptor",
			"conversation_id", desc.ConversationID, "error", err)
	}
}

// Adopt takes back a conversation whose agent kept running on a remote host
// while this server restarted, from the descriptor the host kept for it (see
// pushDescriptor). The agent's ACP session is assumed idle: a fresh connection is
// put on the stream without a handshake, and a session/cancel is sent in case a
// turn was in flight when the old server went away (that turn is lost). Hooks
// are rebuilt from the project's current config; resumable hooks get the state
// their predecessors exported, other session hooks are not set up again.
// Subscribers see types.ConversationAdopted.
//
// An error means the conversation cannot be adopted and its process should be
// stopped.
func (r *Router) Adopt(ctx context.Context, host process.Host, ps process.Handle, raw []byte) (types.ConversationMeta, error) {
	var desc descriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return types.ConversationMeta{}, fmt.Errorf("router: adopt: bad descriptor: %w", err)
	}
	if desc.ConversationID == "" || desc.SessionID == "" {
		return types.ConversationMeta{}, fmt.Errorf("router: adopt %q: the conversation never finished its handshake", desc.ConversationID)
	}
	r.mu.RLock()
	_, exists := r.sessions[desc.ConversationID]
	r.mu.RUnlock()
	if exists {
		return types.ConversationMeta{}, fmt.Errorf("router: adopt %q: conversation is already live", desc.ConversationID)
	}

	opts := desc.opts()
	// Only the hooks are wanted from the project; the rest of opts is what the
	// conversation actually started with, kept in the descriptor.
	probe := opts
	hooks, _, err := r.resolveProject(ctx, &probe)
	if err != nil {
		return types.ConversationMeta{}, err
	}
	states := make(map[int]hookState, len(desc.Hooks))
	for _, hs := range desc.Hooks {
		states[hs.Index] = hs
	}
	sc := hook.SessionContext{
		Meta:           types.ConversationMeta{ProjectID: opts.ProjectID, ConversationID: desc.ConversationID},
		ConversationID: desc.ConversationID,
		BaseDir:        desc.BaseDir,
		Host:           host,
	}
	var cleanups []func(bool)
	for i, h := range hooks {
		rh, ok := h.(hook.Resumable)
		if !ok {
			continue
		}
		hs, ok := states[i]
		if !ok || hs.Type != fmt.Sprintf("%T", h) {
			hs = hookState{}
		}
		cleanup, err := rh.ResumeSession(sc, hs.State)
		if err != nil {
			slog.Warn("router: adopt: hook could not resume", "conversation_id", desc.ConversationID,
				"hook", fmt.Sprintf("%T", h), "error", err)
			continue
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}
	var guards []hook.CloseGuard
	for _, h := range hooks {
		if g, ok := h.(hook.CloseGuard); ok {
			guards = append(guards, g)
		}
	}

	meta := types.ConversationMeta{
		ProjectID:      opts.ProjectID,
		ProcessPID:     ps.PID(),
		ConversationID: desc.ConversationID,
		SessionID:      acp.SessionId(desc.SessionID),
	}
	streamID := desc.StreamID
	if streamID == "" {
		streamID = desc.ConversationID
	}
	state := &SessionState{
		meta:        meta,
		sessionData: acp.NewSessionResponse{SessionId: meta.SessionID},
		proc:        ps,
		host:        host,
		streamID:    streamID,
		opts:        opts,
		hooks:       hooks,
		baseDir:     desc.BaseDir,
		cleanups:    cleanups,
		guards:      guards,
	}
	handler := func(ctx context.Context, rid *json.RawMessage, msg any) {
		r.onMessage(ctx, state, rid, msg)
	}
	stdin, stdout := ps.Stdio()
	state.connection = acp.NewClientSideConnection(handler, stdin, stdout)

	r.mu.Lock()
	if _, exists := r.sessions[desc.ConversationID]; exists {
		r.mu.Unlock()
		return types.ConversationMeta{}, fmt.Errorf("router: adopt %q: conversation is already live", desc.ConversationID)
	}
	r.sessions[desc.ConversationID] = state
	r.mu.Unlock()

	r.Receive(ctx, nil, meta, types.ConversationAdopted{Meta: meta})
	slog.Info("router: adopted conversation from remote host",
		"conversation_id", meta.ConversationID, "location", host.Name(), "pid", meta.ProcessPID)

	// A notification, so it is not answered and needs no request id bookkeeping.
	if err := state.connection.Send(ctx, acp.CancelNotification{SessionId: meta.SessionID}); err != nil {
		slog.Warn("router: adopt: session/cancel failed", "conversation_id", meta.ConversationID, "error", err)
	}
	go r.watchProcess(state, ps)
	return meta, nil
}

// Finalize records that a conversation this server no longer tracks has ended:
// a remote host reports a process that expired while no server was connected.
// Subscribers get a ConversationClosed so the session row stops showing as
// running. It is a no-op for a live conversation.
func (r *Router) Finalize(raw []byte, errMsg string) {
	var desc descriptor
	if err := json.Unmarshal(raw, &desc); err != nil || desc.ConversationID == "" {
		return
	}
	if r.Active(desc.ConversationID) {
		return
	}
	meta := types.ConversationMeta{
		ProjectID:      desc.ProjectID,
		ConversationID: desc.ConversationID,
		SessionID:      acp.SessionId(desc.SessionID),
	}
	r.Receive(r.ctx, nil, meta, types.ConversationClosed{Meta: meta, Err: errMsg})
}
