package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/elek/acpp/db"
)

func doPostJSON(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

func projectNames(t *testing.T, store db.ProjectStore) []string {
	t.Helper()
	rows, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestCreateProjectNoDir(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/projects", `{"name":"widgets"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if !hasName(projectNames(t, store), "widgets") {
		t.Errorf("project %q not persisted", "widgets")
	}
}

func TestCreateProjectWithDir(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)
	dir := t.TempDir()

	rec := doPostJSON(t, s, "/projects", `{"name":"widgets","dir":"`+dir+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	p, err := store.GetProject(context.Background(), "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != dir {
		t.Errorf("dir = %q, want %q", p.Dir, dir)
	}
}

func TestCreateProjectMissingDir(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/projects", `{"name":"widgets","dir":"/no/such/dir/here"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if hasName(projectNames(t, store), "widgets") {
		t.Errorf("project should not be created when dir is invalid")
	}
}

func TestCreateProjectEmptyName(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/projects", `{"name":"  "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateProjectDuplicate(t *testing.T) {
	store := db.NewMemStore()
	ctx := context.Background()
	if err := store.SetProjectField(ctx, "widgets", "dir", "/existing/dir"); err != nil {
		t.Fatal(err)
	}
	s := New(store, ":0").WithProjects(store)

	// A second create with a (valid-looking but different) dir must not clobber.
	rec := doPostJSON(t, s, "/projects", `{"name":"widgets"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	p, err := store.GetProject(ctx, "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != "/existing/dir" {
		t.Errorf("dir = %q, want existing %q untouched", p.Dir, "/existing/dir")
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["existed"] != true {
		t.Errorf("existed = %v, want true", body["existed"])
	}
}

func TestViewProjectsRendersNewProjectButton(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/projects")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="new-project-btn"`) {
		t.Errorf("new project button not rendered")
	}
	if !strings.Contains(rec.Body.String(), `id="np-overlay"`) {
		t.Errorf("new project modal not rendered")
	}
}

func TestViewProjectsNoButtonWithoutStore(t *testing.T) {
	s := New(db.NewMemStore(), ":0") // no WithProjects
	rec := doGet(t, s, "/projects")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `id="new-project-btn"`) {
		t.Errorf("new project button should be hidden without a project store")
	}
}

func TestCreateProjectNoStore(t *testing.T) {
	s := New(db.NewMemStore(), ":0") // no WithProjects
	rec := doPostJSON(t, s, "/projects", `{"name":"widgets"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestViewProjectsRendersSessionInfoPanel(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/projects?project=acpp&session=s2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The info toggle button and the side panel are both rendered when a
	// session is active.
	if !strings.Contains(body, `id="info-btn"`) {
		t.Errorf("session info toggle button not rendered")
	}
	if !strings.Contains(body, `id="session-info"`) {
		t.Errorf("session info side panel not rendered")
	}
	if !strings.Contains(body, "Session Info") {
		t.Errorf("session info title not rendered")
	}
	// Context window: used = 100000 + 3800 = 103800 of 200000 (claude) => 51%.
	if !strings.Contains(body, "103.8K / 200K") {
		t.Errorf("context window count not rendered:\n%s", body)
	}
	if !strings.Contains(body, "51% used") {
		t.Errorf("context window percent not rendered")
	}
	if !strings.Contains(body, "claude-sonnet-4.6") {
		t.Errorf("model not rendered in info panel")
	}
	// Sandbox type (from the session row) and profiles (from the project) are
	// surfaced in the panel.
	if !strings.Contains(body, ">Sandbox<") {
		t.Errorf("sandbox label not rendered in info panel")
	}
	if !strings.Contains(body, "bbwrap") {
		t.Errorf("sandbox type not rendered in info panel:\n%s", body)
	}
	if !strings.Contains(body, ">Profiles<") {
		t.Errorf("profiles label not rendered in info panel")
	}
	if !strings.Contains(body, "docker,ssh") {
		t.Errorf("sandbox profiles not rendered in info panel:\n%s", body)
	}
}

func TestViewProjectsNoSessionInfoWithoutSession(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/projects")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `id="session-info"`) {
		t.Errorf("session info panel should not render without an active session")
	}
}

func TestViewTaskbarFragment(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/projects/taskbar?project=acpp&session=s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// It is a fragment, not the whole page.
	if strings.Contains(body, "<!DOCTYPE") || strings.Contains(body, "id=\"new-project-btn\"") {
		t.Errorf("taskbar endpoint returned a full page, want just the tabs fragment:\n%s", body)
	}
	// The active running session appears as an active dot; the completed one is
	// filtered out of the live bar.
	if !strings.Contains(body, "session=s1") {
		t.Errorf("running session s1 not rendered in taskbar:\n%s", body)
	}
	if !strings.Contains(body, "status-dot-running") {
		t.Errorf("running dot class missing:\n%s", body)
	}
	if !strings.Contains(body, "taskbar-dot status-dot-running active") {
		t.Errorf("active session s1 not marked active:\n%s", body)
	}
	if strings.Contains(body, "session=s2") {
		t.Errorf("completed session s2 should be filtered out of the taskbar:\n%s", body)
	}
}

func TestViewProjectDetail(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/project/acpp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Both sections render, split by status.
	if !strings.Contains(body, "Active Sessions") || !strings.Contains(body, "Closed Sessions") {
		t.Errorf("both session sections should render:\n%s", body)
	}
	// The running session (s1) opens as a card, the completed one (s2) too.
	if !strings.Contains(body, "session=s1") {
		t.Errorf("active session card for s1 missing:\n%s", body)
	}
	if !strings.Contains(body, "session=s2") {
		t.Errorf("closed session card for s2 missing:\n%s", body)
	}
	// The closed session's first prompt is previewed on its card.
	if !strings.Contains(body, "Add /help command") {
		t.Errorf("prompt preview for s2 missing:\n%s", body)
	}
	// Configuration surfaces the project's stored fields.
	if !strings.Contains(body, "Configuration") || !strings.Contains(body, ">agent<") {
		t.Errorf("configuration table missing project fields:\n%s", body)
	}
	// The nav uses the renamed "Workplace" label.
	if !strings.Contains(body, ">Workplace</a>") {
		t.Errorf("nav should use the Workplace label:\n%s", body)
	}
}
