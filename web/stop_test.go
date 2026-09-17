package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/elek/acpp/db"
	"github.com/elek/acpp/hook"
	acplib "github.com/elek/acpp/types"
)

// fakeCloser stands in for the WebChannel: closing a session finalizes its row
// the way the persistence subscriber does — synchronously, before CloseSession
// returns — so the handler sees the same post-close store state as it does in
// production.
type fakeCloser struct {
	store *db.MemStore
	// refuse, when non-empty, is the reason a close guard gives for vetoing an
	// unforced close — the worktree hook's "uncommitted changes" case.
	refuse string
	closed []string
	forced []string
}

func (f *fakeCloser) CloseSession(sessionID string, force bool) error {
	if f.refuse != "" && !force {
		return &hook.CloseRefusedError{Reason: f.refuse}
	}
	f.closed = append(f.closed, sessionID)
	if force {
		f.forced = append(f.forced, sessionID)
	}
	_ = f.store.FinishSession(context.Background(), sessionID, acplib.StatusInfo{Status: acplib.StatusComplete}, "")
	return nil
}

// doStop posts a stop the way the project window does: with fetch, asking for
// JSON. referer mimics the window the click came from.
func doStop(t *testing.T, s *Server, id, referer string, acceptJSON bool) *httptest.ResponseRecorder {
	t.Helper()
	return doStopURL(t, s, "/session/"+id+"/stop", referer, acceptJSON)
}

func doStopURL(t *testing.T, s *Server, url, referer string, acceptJSON bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, url, nil)
	if acceptJSON {
		req.Header.Set("Accept", "application/json")
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

func projectLive(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var body map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	live, ok := body["project_live"]
	if !ok {
		t.Fatalf("response has no project_live field: %s", rec.Body.String())
	}
	return live
}

// Stopping the project's only open session leaves it with nothing live, which is
// what tells the window to close instead of staying on the dead conversation.
// The project's finished history (s2) must not count as live.
func TestStopSessionReportsProjectWentEmpty(t *testing.T) {
	store := seedStore(t)
	closer := &fakeCloser{store: store}
	s := New(store, ":0").WithProjects(store).WithCloser(closer)

	rec := doStop(t, s, "s1", "/projects?project=acpp&session=s1", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(closer.closed) != 1 || closer.closed[0] != "s1" {
		t.Fatalf("closed = %v, want [s1]", closer.closed)
	}
	if projectLive(t, rec) {
		t.Error("project_live = true, want false with only finished sessions left")
	}
}

// With another session of the same project still open the window has work left
// to show, so it stays.
func TestStopSessionKeepsWindowWhileAnotherSessionIsOpen(t *testing.T) {
	store := seedStore(t)
	ctx := context.Background()
	if err := store.InsertSession(ctx, "s3", "web", "claude", "/tmp/does-not-exist/acpp", "", "", "", "acpp", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	closer := &fakeCloser{store: store}
	s := New(store, ":0").WithProjects(store).WithCloser(closer)

	rec := doStop(t, s, "s1", "/projects?project=acpp&session=s1", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !projectLive(t, rec) {
		t.Error("project_live = false, want true while s3 is still pending")
	}
}

// A session recorded without a project cannot be reasoned about, so the window
// is left alone rather than closed on a guess.
func TestStopSessionWithoutProjectKeepsWindow(t *testing.T) {
	store := db.NewMemStore()
	ctx := context.Background()
	if err := store.InsertSession(ctx, "loose", "web", "claude", "/tmp/loose", "", "", "", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	closer := &fakeCloser{store: store}
	s := New(store, ":0").WithProjects(store).WithCloser(closer)

	rec := doStop(t, s, "loose", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !projectLive(t, rec) {
		t.Error("project_live = false, want true for a session with no project")
	}
}

// The session detail page stops with a plain form post: it must keep getting the
// redirect back to where it came from.
func TestStopSessionFormPostStillRedirects(t *testing.T) {
	store := seedStore(t)
	closer := &fakeCloser{store: store}
	s := New(store, ":0").WithProjects(store).WithCloser(closer)

	rec := doStop(t, s, "s1", "/session/s1", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/session/s1" {
		t.Errorf("Location = %q, want the referer /session/s1", loc)
	}
}

// A session whose ephemeral worktree holds uncommitted work is not stopped: the
// click comes back as a refusal the user can act on, and nothing is closed.
func TestStopSessionRefusedWhileWorktreeIsDirty(t *testing.T) {
	store := seedStore(t)
	closer := &fakeCloser{store: store, refuse: "the isolated worktree /repo/.worktree/s1 has uncommitted changes: notes.txt"}
	s := New(store, ":0").WithProjects(store).WithCloser(closer)

	rec := doStop(t, s, "s1", "/projects?project=acpp&session=s1", true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if len(closer.closed) != 0 {
		t.Errorf("closed = %v, want nothing closed on a refusal", closer.closed)
	}

	var body struct {
		Error   string `json:"error"`
		Refused bool   `json:"refused"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	if !body.Refused {
		t.Error("refused = false, want true so the client knows to offer Stop anyway")
	}
	if !strings.Contains(body.Error, "notes.txt") {
		t.Errorf("error = %q, want the guard's reason naming the uncommitted file", body.Error)
	}
}

// "Stop anyway" overrules the refusal: the same click with force=1 closes.
func TestStopSessionForceOverrulesRefusal(t *testing.T) {
	store := seedStore(t)
	closer := &fakeCloser{store: store, refuse: "uncommitted changes"}
	s := New(store, ":0").WithProjects(store).WithCloser(closer)

	rec := doStopURL(t, s, "/session/s1/stop?force=1", "/projects?project=acpp&session=s1", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(closer.forced) != 1 || closer.forced[0] != "s1" {
		t.Fatalf("forced = %v, want [s1]", closer.forced)
	}
	if projectLive(t, rec) {
		t.Error("project_live = true, want false — a forced stop reports emptiness like any other")
	}
}

// A browser with no JavaScript posts the plain form and must still be told why
// the stop did not happen, rather than silently appearing to succeed.
func TestStopSessionRefusalOnFormPost(t *testing.T) {
	store := seedStore(t)
	closer := &fakeCloser{store: store, refuse: "the isolated worktree has uncommitted changes: notes.txt"}
	s := New(store, ":0").WithProjects(store).WithCloser(closer)

	rec := doStop(t, s, "s1", "/session/s1", false)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "notes.txt") {
		t.Errorf("body = %q, want the guard's reason", rec.Body.String())
	}
	if len(closer.closed) != 0 {
		t.Errorf("closed = %v, want nothing closed", closer.closed)
	}
}

func TestStopSessionWithoutCloser(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store) // no WithCloser

	rec := doStop(t, s, "s1", "", true)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}
