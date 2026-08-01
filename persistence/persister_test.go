package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
)

// feed runs a sequence of router messages through the persister for a single
// conversation, returning the resulting session row.
func feed(t *testing.T, p *Persister, store *db.MemStore, meta types.ConversationMeta, msgs ...any) db.SessionRow {
	t.Helper()
	ctx := context.Background()
	// A conversation is always created before any updates flow: this is what
	// writes the session row (keyed by ConversationID), independent of the ACP
	// handshake.
	p.Receive(ctx, nil, meta, types.ConversationCreated{Meta: meta})
	for _, m := range msgs {
		p.Receive(ctx, nil, meta, m)
	}
	row, err := store.GetSession(ctx, meta.ConversationID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	return row
}

func TestPersister_PopulatesModelAndUsageOnTurn(t *testing.T) {
	store := db.NewMemStore()
	p := New(router.New(), store)

	meta := types.ConversationMeta{ConversationID: "conv-1", SessionID: acp.SessionId("sess-1")}

	chunk := acp.SessionNotification{
		SessionId: meta.SessionID,
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Meta: map[string]any{
					"claudeCode": map[string]any{
						"model": "claude-opus-4-8",
						"modelUsage": map[string]any{
							"claude-opus-4-8": map[string]any{
								"inputTokens":  float64(100),
								"outputTokens": float64(200),
							},
						},
						"totalCostUsd": float64(0.5),
					},
				},
			},
		},
	}

	row := feed(t, p, store,
		meta,
		acp.NewSessionResponse{SessionId: meta.SessionID},
		acp.PromptRequest{SessionId: meta.SessionID},
		chunk,
		acp.PromptResponse{StopReason: acp.StopReason("end_turn")},
	)

	if row.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want claude-opus-4-8", row.Model)
	}
	if row.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", row.InputTokens)
	}
	if row.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", row.OutputTokens)
	}
	if row.CostUSD != 0.5 {
		t.Errorf("CostUSD = %v, want 0.5", row.CostUSD)
	}
	if row.PromptCount != 1 {
		t.Errorf("PromptCount = %d, want 1", row.PromptCount)
	}
	if row.Status != string(types.StatusRunning) {
		t.Errorf("Status = %q, want running", row.Status)
	}
}

// TestPersister_RunningOnTurnStart verifies the persisted status flips to
// running as soon as a turn begins (PromptRequest), before the response arrives.
// Otherwise a conversation that is actively processing its first prompt is shown
// as "pending" for the whole turn.
func TestPersister_RunningOnTurnStart(t *testing.T) {
	store := db.NewMemStore()
	p := New(router.New(), store)

	meta := types.ConversationMeta{ConversationID: "conv-run", SessionID: acp.SessionId("sess-run")}

	// Feed only up to PromptRequest: the turn is in progress, no response yet.
	row := feed(t, p, store,
		meta,
		acp.NewSessionResponse{SessionId: meta.SessionID},
		acp.PromptRequest{SessionId: meta.SessionID},
	)

	if row.Status != string(types.StatusRunning) {
		t.Errorf("Status = %q, want running (turn in progress)", row.Status)
	}
}

func TestPersister_AccumulatesPromptDuration(t *testing.T) {
	store := db.NewMemStore()
	p := New(router.New(), store)
	// Deterministic clock: each call advances by one second.
	base := time.Unix(1000, 0)
	var ticks int64
	p.now = func() time.Time {
		ticks++
		return base.Add(time.Duration(ticks) * time.Second)
	}

	meta := types.ConversationMeta{ConversationID: "conv-2", SessionID: acp.SessionId("sess-2")}

	row := feed(t, p, store,
		meta,
		acp.NewSessionResponse{SessionId: meta.SessionID},
		acp.PromptRequest{SessionId: meta.SessionID}, // now() -> 1001s (start)
		acp.PromptResponse{},                         // now() -> 1002s (end) => 1000ms
	)

	if row.PromptDurationMs != 1000 {
		t.Errorf("PromptDurationMs = %d, want 1000", row.PromptDurationMs)
	}
}

func TestPersister_FinishOnClose(t *testing.T) {
	store := db.NewMemStore()
	p := New(router.New(), store)

	meta := types.ConversationMeta{ConversationID: "conv-3", SessionID: acp.SessionId("sess-3")}

	row := feed(t, p, store,
		meta,
		acp.NewSessionResponse{SessionId: meta.SessionID},
		acp.PromptRequest{SessionId: meta.SessionID},
		acp.PromptResponse{},
		types.ConversationClosed{Meta: meta},
	)

	if row.Status != string(types.StatusComplete) {
		t.Errorf("Status = %q, want complete", row.Status)
	}
	if row.FinishedAt == nil {
		t.Error("FinishedAt = nil, want a timestamp")
	}
}

// TestPersister_SetsACPSessionID verifies the ACP session id from the handshake
// is recorded onto the conversation-keyed row.
func TestPersister_SetsACPSessionID(t *testing.T) {
	store := db.NewMemStore()
	p := New(router.New(), store)

	meta := types.ConversationMeta{ConversationID: "conv-acp", SessionID: acp.SessionId("acp-xyz")}
	row := feed(t, p, store, meta, acp.NewSessionResponse{SessionId: meta.SessionID})

	if row.ID != "conv-acp" {
		t.Errorf("row id = %q, want conv-acp (the conversation id)", row.ID)
	}
	if row.ACPSessionID != "acp-xyz" {
		t.Errorf("ACPSessionID = %q, want acp-xyz", row.ACPSessionID)
	}
}

// TestPersister_StillbornErrorFlow verifies a conversation created and closed
// without ever completing an ACP handshake still persists an errored row with the
// harness error message logged against it and no ACP session id.
func TestPersister_StillbornErrorFlow(t *testing.T) {
	store := db.NewMemStore()
	p := New(router.New(), store)
	ctx := context.Background()

	meta := types.ConversationMeta{ConversationID: "conv-fail"}
	p.Receive(ctx, nil, meta, types.ConversationCreated{Meta: meta})
	p.Receive(ctx, nil, meta, acp.SessionNotification{
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Meta:    map[string]any{"acpp": map[string]any{"type": "error"}},
				Content: acp.TextBlock("no directory found"),
			},
		},
	})
	p.Receive(ctx, nil, meta, types.ConversationClosed{Meta: meta, Err: "no directory found"})

	row, err := store.GetSession(ctx, "conv-fail")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.Status != string(types.StatusError) {
		t.Errorf("Status = %q, want error", row.Status)
	}
	if row.ACPSessionID != "" {
		t.Errorf("ACPSessionID = %q, want empty (stillborn)", row.ACPSessionID)
	}
	if row.FinishedAt == nil {
		t.Error("FinishedAt = nil, want a timestamp")
	}

	logs, err := store.GetSessionLogs(ctx, "conv-fail")
	if err != nil {
		t.Fatalf("GetSessionLogs: %v", err)
	}
	var found bool
	for _, l := range logs {
		if l.EventType == "agent_message_chunk" {
			found = true
		}
	}
	if !found {
		t.Error("expected an agent_message_chunk log carrying the error message")
	}
}

// TestPersister_PersistsContextWindowUsage verifies the authoritative context
// occupancy from a usage_update is persisted onto the row — and that it survives
// the whole-Usage replacement that a subsequent modelUsage-carrying chunk and
// prompt response perform (which would otherwise wipe ContextUsed back to zero).
func TestPersister_PersistsContextWindowUsage(t *testing.T) {
	store := db.NewMemStore()
	p := New(router.New(), store)

	meta := types.ConversationMeta{ConversationID: "conv-ctx", SessionID: acp.SessionId("sess-ctx")}

	usage := acp.SessionNotification{
		SessionId: meta.SessionID,
		Update: acp.SessionUpdate{
			UsageUpdate: &acp.SessionUsageUpdate{Size: 200000, Used: 150000},
		},
	}
	// A chunk carrying cumulative modelUsage arrives AFTER the usage_update and
	// replaces the whole Usage struct in memory.
	chunk := acp.SessionNotification{
		SessionId: meta.SessionID,
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Meta: map[string]any{
					"claudeCode": map[string]any{
						"model": "claude-opus-4-8",
						"modelUsage": map[string]any{
							"claude-opus-4-8": map[string]any{
								"inputTokens":          float64(300300),
								"cacheReadInputTokens": float64(62000),
							},
						},
					},
				},
			},
		},
	}

	row := feed(t, p, store,
		meta,
		acp.NewSessionResponse{SessionId: meta.SessionID},
		acp.PromptRequest{SessionId: meta.SessionID},
		usage,
		chunk,
		acp.PromptResponse{StopReason: acp.StopReason("end_turn")},
	)

	if row.ContextUsed != 150000 {
		t.Errorf("ContextUsed = %d, want 150000 (authoritative occupancy)", row.ContextUsed)
	}
	if row.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", row.ContextWindow)
	}
}

func TestPersister_TypedUsageFromPromptResponse(t *testing.T) {
	store := db.NewMemStore()
	p := New(router.New(), store)

	meta := types.ConversationMeta{ConversationID: "conv-4", SessionID: acp.SessionId("sess-4")}
	in, out := 11, 22
	cachedRead := 5

	row := feed(t, p, store,
		meta,
		acp.NewSessionResponse{SessionId: meta.SessionID},
		acp.PromptRequest{SessionId: meta.SessionID},
		acp.PromptResponse{Usage: &acp.Usage{InputTokens: in, OutputTokens: out, CachedReadTokens: &cachedRead}},
	)

	if row.InputTokens != 11 || row.OutputTokens != 22 {
		t.Errorf("tokens = %d/%d, want 11/22", row.InputTokens, row.OutputTokens)
	}
	if row.CacheReadInputTokens != 5 {
		t.Errorf("CacheReadInputTokens = %d, want 5", row.CacheReadInputTokens)
	}
}
