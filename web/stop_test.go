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

// fakeCloser stands in for the WebChannel: closing a session finalizes its row
// the way the persistence subscriber does — synchronously, before CloseSession
// returns — so the handler sees the same post-close store state as it does in
// production.
type fakeCloser struct {
	store  *db.MemStore
	closed []string
}

func (f *fakeCloser) CloseSession(sessionID string) {
	f.closed = append(f.closed, sessionID)
	_ = f.store.FinishSession(context.Background(), sessionID, acplib.StatusInfo{Status: acplib.StatusComplete}, "")
}

// doStop posts a stop the way the project window does: with fetch, asking for
// JSON. referer mimics the window the click came from.
func doStop(t *testing.T, s *Server, id, referer string, acceptJSON bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/session/"+id+"/stop", nil)
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

func TestStopSessionWithoutCloser(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store) // no WithCloser

	rec := doStop(t, s, "s1", "", true)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}
