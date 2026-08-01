package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/elek/acpp/db"
	acplib "github.com/elek/acpp/types"
)

func doGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

func TestAPIHealth(t *testing.T) {
	s := New(db.NewMemStore(), ":0")
	rec := doGet(t, s, "/api/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
	if _, ok := body["version"]; !ok {
		t.Errorf("missing version field")
	}
}

// seedStore creates one project ("acpp") with two sessions: one running, one
// complete, the complete one carrying a prompt log for title/preview.
func seedStore(t *testing.T) *db.MemStore {
	t.Helper()
	store := db.NewMemStore()
	ctx := context.Background()
	// Use a non-repo dir so the best-effort git lookup yields empty branch/dirty.
	dir := "/tmp/does-not-exist/acpp"
	if err := store.SetProjectField(ctx, "acpp", "dir", dir); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProjectField(ctx, "acpp", "agent", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProjectField(ctx, "acpp", "sandbox_profiles", "docker,ssh"); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := store.InsertSession(ctx, "s1", "web", "claude", dir, "", "", "", "acpp", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSession(ctx, "s1", acplib.StatusInfo{Status: acplib.StatusRunning, Model: "claude-sonnet-4.6"}); err != nil {
		t.Fatal(err)
	}

	if err := store.InsertSession(ctx, "s2", "web", "claude", dir, "bbwrap", "", "", "acpp", nil, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	info := acplib.StatusInfo{Status: acplib.StatusComplete, Model: "claude-sonnet-4.6"}
	// Cumulative lifetime counters (grow across turns; NOT the context occupancy).
	info.Usage.InputTokens = 100000
	info.Usage.CacheReadInputTokens = 3800
	// Authoritative context occupancy reported by the agent's usage_update.
	info.Usage.ContextUsed = 120000
	info.Usage.ContextWindow = 200000
	info.Usage.CostUSD = 1.27
	if err := store.FinishSession(ctx, "s2", info, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertLog(ctx, "s2", "prompt", json.RawMessage(`{"prompt":"Add /help command"}`)); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAPIProjects(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/api/projects")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var projects []ProjectJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &projects); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	p := projects[0]
	if p.Name != "acpp" {
		t.Errorf("name = %q, want acpp", p.Name)
	}
	if p.Agent != "claude" {
		t.Errorf("agent = %q, want claude", p.Agent)
	}
	if p.ChatCount != 2 {
		t.Errorf("chat_count = %d, want 2", p.ChatCount)
	}
	if p.RunningCount != 1 {
		t.Errorf("running_count = %d, want 1", p.RunningCount)
	}
	// Non-repo dir => best-effort git yields empty branch and false dirty.
	if p.Branch != "" {
		t.Errorf("branch = %q, want empty for non-repo dir", p.Branch)
	}
}

func TestBuildProjectTabs(t *testing.T) {
	store := seedStore(t)
	ctx := context.Background()

	// A second project whose only session is finished; it must not appear in the
	// bottom bar because it has no pending/running session.
	dir2 := "/tmp/does-not-exist/idle"
	if err := store.SetProjectField(ctx, "idle", "dir", dir2); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSession(ctx, "i1", "web", "claude", dir2, "", "", "", "idle", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSession(ctx, "i1", acplib.StatusInfo{Status: acplib.StatusComplete}, ""); err != nil {
		t.Fatal(err)
	}

	s := New(store, ":0").WithProjects(store)
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}

	tabs, err := s.buildProjectTabs(ctx, projects, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Only "acpp" has an active session; "idle" is dropped.
	if len(tabs) != 1 {
		t.Fatalf("got %d tabs, want 1: %+v", len(tabs), tabs)
	}
	if tabs[0].Name != "acpp" {
		t.Fatalf("tab name = %q, want acpp", tabs[0].Name)
	}
	// Only the running session s1 is shown; the completed s2 is filtered out.
	if len(tabs[0].Sessions) != 1 {
		t.Fatalf("got %d session dots, want 1: %+v", len(tabs[0].Sessions), tabs[0].Sessions)
	}
	if dot := tabs[0].Sessions[0]; dot.ID != "s1" || dot.Status != "running" {
		t.Fatalf("dot = %+v, want s1/running", dot)
	}

	// The active project stays in the bar even with no active session, so
	// navigating to an idle project doesn't drop its own tab.
	tabs, err = s.buildProjectTabs(ctx, projects, "idle", "")
	if err != nil {
		t.Fatal(err)
	}
	var sawIdle bool
	for _, tab := range tabs {
		if tab.Name == "idle" {
			sawIdle = true
			if len(tab.Sessions) != 0 {
				t.Errorf("idle tab has %d dots, want 0", len(tab.Sessions))
			}
		}
	}
	if !sawIdle {
		t.Errorf("active project 'idle' missing from tabs: %+v", tabs)
	}
}

func TestAPISessions(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/api/sessions?project=acpp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var sessions []SessionJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}

	byID := map[string]SessionJSON{}
	for _, sess := range sessions {
		byID[sess.ID] = sess
	}

	if got := byID["s1"].Status; got != "running" {
		t.Errorf("s1 status = %q, want running", got)
	}
	if got := byID["s2"].Status; got != "done" {
		t.Errorf("s2 status = %q, want done", got)
	}
	if got := byID["s2"].Title; got != "Add /help command" {
		t.Errorf("s2 title = %q, want %q", got, "Add /help command")
	}
	if got := byID["s2"].ContextUsed; got != 120000 {
		t.Errorf("s2 context_used = %d, want 120000 (authoritative, not the cumulative-token sum)", got)
	}
	if got := byID["s2"].ContextWindow; got != 200000 {
		t.Errorf("s2 context_window = %d, want 200000", got)
	}
	if byID["s2"].CostUSD == nil || *byID["s2"].CostUSD != 1.27 {
		t.Errorf("s2 cost_usd = %v, want 1.27", byID["s2"].CostUSD)
	}
	if byID["s1"].CostUSD != nil {
		t.Errorf("s1 cost_usd = %v, want nil (unknown)", byID["s1"].CostUSD)
	}
}

func TestAPISession(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/api/session/s2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var sess SessionJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sess.ID != "s2" {
		t.Errorf("id = %q, want s2", sess.ID)
	}
	if sess.Status != "done" {
		t.Errorf("status = %q, want done", sess.Status)
	}
}

// TestSessionContextPrefersAuthoritativeUsage proves the fix for the "used >
// window" bug: the panel must report the agent's authoritative context
// occupancy (usage_update), never the ever-growing cumulative token counters
// (input + cache_creation + cache_read), which re-count the cached prefix every
// turn and sail past the real window.
func TestSessionContextPrefersAuthoritativeUsage(t *testing.T) {
	store := db.NewMemStore()
	ctx := context.Background()
	dir := "/tmp/does-not-exist/acpp"
	if err := store.SetProjectField(ctx, "acpp", "dir", dir); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.InsertSession(ctx, "big", "web", "claude", dir, "", "", "", "acpp", nil, now); err != nil {
		t.Fatal(err)
	}
	info := acplib.StatusInfo{Status: acplib.StatusRunning, Model: "claude-opus-4-8"}
	// Cumulative counters that would sum to 362.3K — far past the 200K window.
	info.Usage.InputTokens = 300300
	info.Usage.CacheReadInputTokens = 62000
	// Authoritative occupancy from usage_update.
	info.Usage.ContextUsed = 150000
	info.Usage.ContextWindow = 200000
	if err := store.UpdateSession(ctx, "big", info); err != nil {
		t.Fatal(err)
	}

	s := New(store, ":0").WithProjects(store)
	rec := doGet(t, s, "/api/session/big")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var sess SessionJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sess.ContextUsed != 150000 {
		t.Errorf("context_used = %d, want 150000 (authoritative, not the 362300 cumulative sum)", sess.ContextUsed)
	}
	if sess.ContextWindow != 200000 {
		t.Errorf("context_window = %d, want 200000", sess.ContextWindow)
	}
}

// TestSessionContextFallsBackWhenNoUsageUpdate covers agents that never emit a
// usage_update: occupancy is unknown (0, not the misleading cumulative sum) and
// the window falls back to the per-model lookup.
func TestSessionContextFallsBackWhenNoUsageUpdate(t *testing.T) {
	store := db.NewMemStore()
	ctx := context.Background()
	dir := "/tmp/does-not-exist/acpp"
	if err := store.SetProjectField(ctx, "acpp", "dir", dir); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.InsertSession(ctx, "old", "web", "claude", dir, "", "", "", "acpp", nil, now); err != nil {
		t.Fatal(err)
	}
	info := acplib.StatusInfo{Status: acplib.StatusRunning, Model: "claude-sonnet-4.6"}
	info.Usage.InputTokens = 90000
	info.Usage.CacheReadInputTokens = 5000
	// No ContextUsed/ContextWindow — this agent didn't report a usage_update.
	if err := store.UpdateSession(ctx, "old", info); err != nil {
		t.Fatal(err)
	}

	s := New(store, ":0").WithProjects(store)
	rec := doGet(t, s, "/api/session/old")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var sess SessionJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sess.ContextUsed != 0 {
		t.Errorf("context_used = %d, want 0 (occupancy unknown)", sess.ContextUsed)
	}
	if sess.ContextWindow != 200000 {
		t.Errorf("context_window = %d, want 200000 (per-model fallback)", sess.ContextWindow)
	}
}

func TestMapStatus(t *testing.T) {
	cases := map[string]string{
		"running":  "running",
		"pending":  "running",
		"complete": "done",
		"error":    "error",
		"":         "idle",
		"weird":    "idle",
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Errorf("mapStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
