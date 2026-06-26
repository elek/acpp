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
	for _, m := range msgs {
		p.Receive(ctx, nil, meta, m)
	}
	row, err := store.GetSession(ctx, string(meta.SessionID))
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
		acp.PromptResponse{},                          // now() -> 1002s (end) => 1000ms
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
