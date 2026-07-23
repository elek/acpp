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
