// Package persistence records conversation activity to a store. It is an
// independent router.Subscriber: every conversation flowing through the router —
// regardless of which channel started it — has its session row created and its
// updates logged here, decoupled from any channel's transport. Wire it once per
// router that has a store (see persistence.New).
//
// Besides the event log, the persister maintains the session's telemetry
// (model, token/cost usage, prompt count, prompt duration, status). The router
// backs conversations with raw ACP connections and exposes no aggregated status,
// so the persister accumulates this state in memory from the ACP message stream
// — the same extraction the old session backend performed — and flushes it to
// the store on turn and conversation boundaries.
package persistence

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
)

// Persister subscribes to a router and writes conversations and their event logs
// to a db.SessionWriter.
type Persister struct {
	rt    *router.Router
	store db.SessionWriter

	// now returns the current time; overridable in tests for deterministic
	// prompt-duration measurement.
	now func() time.Time

	mu    sync.Mutex
	track map[string]*sessionState // keyed by ACP session id
}

// sessionState is the per-conversation telemetry the persister accumulates from
// the ACP stream between store writes.
type sessionState struct {
	info        types.StatusInfo
	lastModel   string
	promptStart time.Time
	timing      bool
}

// New creates a Persister and subscribes it to the router so every conversation's
// lifecycle is recorded. Subscribe before conversations are created so no early
// updates are missed.
func New(rt *router.Router, store db.SessionWriter) *Persister {
	p := &Persister{
		rt:    rt,
		store: store,
		now:   time.Now,
		track: make(map[string]*sessionState),
	}
	rt.Subscribe(p.Receive)
	return p
}

// Receive records one router event. Sessions are keyed by the stable
// ConversationID (the DB session.id): the row is written when the conversation is
// created — before, and independent of, the ACP handshake — and the ACP session
// id is recorded onto it once session/new arrives. Updates, prompts and turn
// completions append log entries and accumulate telemetry; turn and close
// boundaries flush that telemetry to the store. Failures are logged but never
// block the router's receive loop.
func (p *Persister) Receive(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	cid := id.ConversationID
	switch m := msg.(type) {
	case types.ConversationCreated:
		// Recover the creation options from the router by the conversation's stable id.
		opts, _ := p.rt.Opts(cid)
		p.insertSession(cid, opts)
	case acp.NewSessionResponse:
		// The handshake assigned the ACP session id; record it onto the row that
		// already exists (created at ConversationCreated).
		if err := p.store.SetACPSessionID(context.Background(), cid, string(m.SessionId)); err != nil {
			slog.Error("persistence: set acp session id", "conversation", cid, "error", err)
		}
	case acp.SessionNotification:
		raw, eventType := db.MarshalEvent(m.Update)
		p.insertLog(cid, eventType, raw)
		p.observeUpdate(cid, m.Update)
	case acp.PromptRequest:
		payload, _ := json.Marshal(types.PromptEchoFromBlocks(m.Prompt))
		p.insertLog(cid, "prompt", payload)
		p.beginTurn(cid)
	case acp.PromptResponse:
		p.insertLog(cid, "prompt_finished", json.RawMessage(`{}`))
		p.endTurn(cid, m)
	case types.ConversationReplaced:
		// The old conversation is done; the new one already has its own row from
		// the ConversationCreated fired during Restart.
		p.finish(m.Old.ConversationID, "")
	case types.ConversationClosed:
		p.finish(m.Meta.ConversationID, m.Err)
	case types.ConversationAdopted:
		p.adopt(cid)
	}
}

// sessionGetter is the read access the persister uses, when its store has it,
// to pick up telemetry it did not accumulate itself (see stored).
type sessionGetter interface {
	GetSession(ctx context.Context, id string) (db.SessionRow, error)
}

// stored returns the telemetry already recorded for a session, so a
// conversation that outlived a server restart keeps accumulating from its
// totals instead of overwriting them with what this process has seen. Zero
// when the store cannot be read.
func (p *Persister) stored(sid string) types.StatusInfo {
	g, ok := p.store.(sessionGetter)
	if !ok {
		return types.StatusInfo{}
	}
	row, err := g.GetSession(context.Background(), sid)
	if err != nil {
		return types.StatusInfo{}
	}
	return types.StatusInfo{
		Model:      row.Model,
		SDKVersion: row.SDKVersion,
		PID:        row.PID,
		HasUsage:   row.PromptCount > 0 || row.InputTokens > 0,
		Usage: types.UsageInfo{
			InputTokens:              row.InputTokens,
			OutputTokens:             row.OutputTokens,
			CacheCreationInputTokens: row.CacheCreationInputTokens,
			CacheReadInputTokens:     row.CacheReadInputTokens,
			ContextWindow:            row.ContextWindow,
			ContextUsed:              row.ContextUsed,
			CostUSD:                  row.CostUSD,
			PromptCount:              row.PromptCount,
		},
	}
}

// adopt resumes tracking a conversation taken back after a server restart: its
// row is made live again and its telemetry picks up from the stored totals.
func (p *Persister) adopt(sid string) {
	info := p.stored(sid)
	info.Status = types.StatusPending
	if err := p.store.ReopenSession(context.Background(), sid); err != nil {
		slog.Error("persistence: reopen adopted session", "conversation", sid, "error", err)
	}
	p.mu.Lock()
	p.track[sid] = &sessionState{info: info, lastModel: info.Model}
	p.mu.Unlock()
}

func (p *Persister) insertSession(cid string, opts types.SessionOpts) {
	err := p.store.InsertSession(context.Background(), cid, opts.Source,
		opts.Agent, opts.CWD, opts.SandboxType, opts.Location, "", opts.ProjectID, opts.Env, p.now())
	if err != nil {
		slog.Error("persistence: insert session", "conversation", cid, "error", err)
		return
	}
	p.mu.Lock()
	p.track[cid] = &sessionState{
		info: types.StatusInfo{Status: types.StatusPending},
	}
	p.mu.Unlock()
}

func (p *Persister) insertLog(sessionID, eventType string, payload json.RawMessage) {
	if sessionID == "" {
		return
	}
	if err := p.store.InsertLog(context.Background(), sessionID, eventType, payload); err != nil {
		slog.Error("persistence: insert log", "session", sessionID, "event", eventType, "error", err)
	}
}

// observeUpdate extracts model and cumulative usage from a streamed session
// update. Token/cost figures are accumulated in memory and only written to the
// store at turn boundaries, so per-chunk updates don't hammer the database.
func (p *Persister) observeUpdate(sid string, update acp.SessionUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.track[sid]
	if st == nil {
		return
	}

	if c := update.AgentMessageChunk; c != nil {
		if model := modelFromMeta(c.Meta); model != "" {
			st.lastModel = model
		}
		if u := extractUsageFromMeta(c.Meta); u.InputTokens > 0 || u.OutputTokens > 0 {
			u.PromptCount = st.info.Usage.PromptCount
			st.info.Usage = carryContext(st.info.Usage, u)
		}
	}

	// Newer agents report context window and cost via a typed usage_update
	// notification instead of embedding cumulative modelUsage in _meta.
	if u := update.UsageUpdate; u != nil {
		st.info.Usage.ContextWindow = int64(u.Size)
		st.info.Usage.ContextUsed = int64(u.Used)
		if u.Cost != nil {
			st.info.Usage.CostUSD = u.Cost.Amount
		}
	}
}

// beginTurn marks the session running and records the turn's start time so its
// duration can be measured when the prompt response arrives. The running status
// is flushed to the store immediately so a conversation actively processing its
// turn is shown as running, not left at its pre-turn "pending" state until the
// response lands.
func (p *Persister) beginTurn(sid string) {
	if sid == "" {
		return
	}
	p.mu.Lock()
	st := p.track[sid]
	if st == nil {
		st = &sessionState{}
		p.track[sid] = st
	}
	st.info.Status = types.StatusRunning
	st.info.Model = st.lastModel
	st.promptStart = p.now()
	st.timing = true
	info := st.info
	p.mu.Unlock()

	if err := p.store.UpdateSession(context.Background(), sid, info); err != nil {
		slog.Error("persistence: update session", "session", sid, "error", err)
	}
}

// endTurn finalizes one prompt turn: it folds in the authoritative usage from the
// prompt response, increments the prompt count, accumulates the turn duration and
// flushes the running session telemetry to the store.
func (p *Persister) endTurn(sid string, resp acp.PromptResponse) {
	if sid == "" {
		return
	}
	p.mu.Lock()
	st := p.track[sid]
	if st == nil {
		st = &sessionState{}
		p.track[sid] = st
	}
	st.info.Usage.PromptCount++

	if model := modelFromMeta(resp.Meta); model != "" && st.lastModel == "" {
		st.lastModel = model
	}
	// The prompt response carries the authoritative cumulative usage: prefer the
	// _meta modelUsage, then the typed Usage value from newer agents.
	if u := extractUsageFromMeta(resp.Meta); u.InputTokens > 0 || u.OutputTokens > 0 {
		count := st.info.Usage.PromptCount
		st.info.Usage = carryContext(st.info.Usage, u)
		st.info.Usage.PromptCount = count
	} else if u := resp.Usage; u != nil {
		st.info.Usage.InputTokens = int64(u.InputTokens)
		st.info.Usage.OutputTokens = int64(u.OutputTokens)
		if u.CachedReadTokens != nil {
			st.info.Usage.CacheReadInputTokens = int64(*u.CachedReadTokens)
		}
		if u.CachedWriteTokens != nil {
			st.info.Usage.CacheCreationInputTokens = int64(*u.CachedWriteTokens)
		}
	}

	st.info.Status = types.StatusRunning
	st.info.Model = st.lastModel

	var durationMs int64
	if st.timing {
		durationMs = p.now().Sub(st.promptStart).Milliseconds()
		st.timing = false
	}
	info := st.info
	p.mu.Unlock()

	if durationMs > 0 {
		if err := p.store.AddPromptDuration(context.Background(), sid, durationMs); err != nil {
			slog.Error("persistence: add prompt duration", "session", sid, "error", err)
		}
	}
	if err := p.store.UpdateSession(context.Background(), sid, info); err != nil {
		slog.Error("persistence: update session", "session", sid, "error", err)
	}
}

// finish marks a session complete (or errored) and stamps finished_at, flushing
// the final accumulated telemetry. Called when the conversation is closed or
// replaced. Safe to call for an unknown session.
func (p *Persister) finish(sid, sessionError string) {
	if sid == "" {
		return
	}
	p.mu.Lock()
	st := p.track[sid]
	var info types.StatusInfo
	if st != nil {
		info = st.info
	}
	delete(p.track, sid)
	p.mu.Unlock()
	if st == nil {
		// Not tracked here (it ended on a remote host while no server was
		// connected): keep the totals already stored.
		info = p.stored(sid)
	}
	if sessionError != "" {
		info.Status = types.StatusError
	} else {
		info.Status = types.StatusComplete
	}

	if err := p.store.FinishSession(context.Background(), sid, info, sessionError); err != nil {
		slog.Error("persistence: finish session", "session", sid, "error", err)
	}
}

// metaKeys are the top-level _meta keys that may carry model/usage data.
// "claudeCode", "rai", "codex", and "gemini" all use the same inner structure.
var metaKeys = []string{"claudeCode", "rai", "codex", "gemini"}

// modelFromMeta returns the model id embedded in a message's _meta, if any.
func modelFromMeta(meta map[string]any) string {
	for _, key := range metaKeys {
		if model := getMetaString(meta, key, "model"); model != "" {
			return model
		}
	}
	return ""
}

// extractUsageFromMeta extracts cumulative usage data from ACP _meta, summing
// across all models within modelUsage.
// carryContext preserves the authoritative context-window occupancy across a
// full Usage replacement. modelUsage-based _meta reports cumulative token
// counters and sometimes the window size, but never the current "used" amount —
// that only arrives via a typed usage_update. Without this carry-over, a chunk
// or prompt response that lands after the usage_update would reset ContextUsed
// to zero, dropping the one figure the context-window display depends on.
func carryContext(prev, next types.UsageInfo) types.UsageInfo {
	if next.ContextUsed == 0 {
		next.ContextUsed = prev.ContextUsed
	}
	if next.ContextWindow == 0 {
		next.ContextWindow = prev.ContextWindow
	}
	return next
}

func extractUsageFromMeta(meta map[string]any) types.UsageInfo {
	var info types.UsageInfo
	var section map[string]any
	for _, key := range metaKeys {
		if s, ok := meta[key].(map[string]any); ok {
			section = s
			break
		}
	}
	if section == nil {
		return info
	}

	if v, ok := section["totalCostUsd"].(float64); ok {
		info.CostUSD = v
	}

	modelUsage, ok := section["modelUsage"].(map[string]any)
	if !ok {
		return info
	}
	for _, usage := range modelUsage {
		m, ok := usage.(map[string]any)
		if !ok {
			continue
		}
		if v, ok := m["inputTokens"].(float64); ok {
			info.InputTokens += int64(v)
		}
		if v, ok := m["outputTokens"].(float64); ok {
			info.OutputTokens += int64(v)
		}
		if v, ok := m["cacheCreationInputTokens"].(float64); ok {
			info.CacheCreationInputTokens += int64(v)
		}
		if v, ok := m["cacheReadInputTokens"].(float64); ok {
			info.CacheReadInputTokens += int64(v)
		}
		if v, ok := m["contextWindow"].(float64); ok {
			info.ContextWindow = int64(v)
		}
		if v, ok := m["maxOutputTokens"].(float64); ok {
			info.MaxOutputTokens = int64(v)
		}
		if v, ok := m["webSearchRequests"].(float64); ok {
			info.WebSearchRequests += int64(v)
		}
		if v, ok := m["costUSD"].(float64); ok && info.CostUSD == 0 {
			info.CostUSD += v
		}
	}
	return info
}

// getMetaString walks a nested map[string]any by the given keys and returns the
// final value if it is a string.
func getMetaString(meta map[string]any, keys ...string) string {
	if meta == nil || len(keys) == 0 {
		return ""
	}
	current := any(meta)
	for i, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		val, exists := m[key]
		if !exists {
			return ""
		}
		if i == len(keys)-1 {
			if s, ok := val.(string); ok {
				return s
			}
			return ""
		}
		current = val
	}
	return ""
}
