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

	now := time.Now()
	if err := store.InsertSession(ctx, "s1", "web", "claude", dir, "", "", "", "acpp", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSession(ctx, "s1", acplib.StatusInfo{Status: acplib.StatusRunning, Model: "claude-sonnet-4.6"}); err != nil {
		t.Fatal(err)
	}

	if err := store.InsertSession(ctx, "s2", "web", "claude", dir, "", "", "", "acpp", nil, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	info := acplib.StatusInfo{Status: acplib.StatusComplete, Model: "claude-sonnet-4.6"}
	info.Usage.InputTokens = 100000
	info.Usage.CacheReadInputTokens = 3800
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
	if got := byID["s2"].ContextUsed; got != 103800 {
		t.Errorf("s2 context_used = %d, want 103800", got)
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
