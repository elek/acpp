package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/elek/acpp/db"
)

func projectRow(t *testing.T, store db.ProjectStore, name string) db.ProjectRow {
	t.Helper()
	p, err := store.GetProject(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetProjectConfigSandboxProfiles(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"sandbox_profiles","value":"docker,ssh"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := projectRow(t, store, "widgets").SandboxProfiles; got != "docker,ssh" {
		t.Errorf("sandbox_profiles = %q, want %q", got, "docker,ssh")
	}

	// The saved value comes back so the page can update the row without a reload.
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["value"] != "docker,ssh" {
		t.Errorf("response value = %q, want %q", body["value"], "docker,ssh")
	}
}

func TestSetProjectConfigSandboxEnv(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	const value = "GITHUB_TOKEN,-ANTHROPIC_API_KEY"
	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"sandbox_env","value":"`+value+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := projectRow(t, store, "widgets").SandboxEnv; got != value {
		t.Errorf("sandbox_env = %q, want %q", got, value)
	}
}

func TestSetProjectConfigTrimsValue(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"agent","value":"  claude  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := projectRow(t, store, "widgets").Agent; got != "claude" {
		t.Errorf("agent = %q, want %q", got, "claude")
	}
}

func TestSetProjectConfigClearsValue(t *testing.T) {
	store := db.NewMemStore()
	if err := store.SetProjectField(context.Background(), "widgets", "sandbox_profiles", "docker"); err != nil {
		t.Fatal(err)
	}
	s := New(store, ":0").WithProjects(store)

	// Unticking every box must be able to return the project to the default.
	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"sandbox_profiles","value":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := projectRow(t, store, "widgets").SandboxProfiles; got != "" {
		t.Errorf("sandbox_profiles = %q, want empty", got)
	}
}

// An unknown profile name is stored rather than rejected: the popup warns about
// it, and blocking would defeat its escape hatch for values the server cannot
// enumerate.
func TestSetProjectConfigAcceptsUnknownProfile(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"sandbox_profiles","value":"not-a-real-profile"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := projectRow(t, store, "widgets").SandboxProfiles; got != "not-a-real-profile" {
		t.Errorf("sandbox_profiles = %q, want the value stored verbatim", got)
	}
}

func TestSetProjectConfigHooks(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"hooks","value":"commit,worktree"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := projectRow(t, store, "widgets").Hooks; got != "commit,worktree" {
		t.Errorf("hooks = %q, want %q", got, "commit,worktree")
	}
}

func TestSetProjectConfigEnvReplacesList(t *testing.T) {
	store := db.NewMemStore()
	ctx := context.Background()
	if err := store.AppendProjectEnv(ctx, "widgets", "OLD=1"); err != nil {
		t.Fatal(err)
	}
	s := New(store, ":0").WithProjects(store)

	// Blank lines are dropped, and the save replaces rather than appends.
	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"env","value":"A=1\n\n  B=2  \n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := projectRow(t, store, "widgets").Env
	if len(got) != 2 || got[0] != "A=1" || got[1] != "B=2" {
		t.Errorf("env = %v, want [A=1 B=2]", got)
	}
}

func TestSetProjectConfigEnvEmptyClears(t *testing.T) {
	store := db.NewMemStore()
	if err := store.AppendProjectEnv(context.Background(), "widgets", "OLD=1"); err != nil {
		t.Fatal(err)
	}
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"env","value":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := projectRow(t, store, "widgets").Env; len(got) != 0 {
		t.Errorf("env = %v, want empty", got)
	}
}

func TestSetProjectConfigInvalidField(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"created_at","value":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestSetProjectConfigNoStore(t *testing.T) {
	s := New(db.NewMemStore(), ":0")

	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":"agent","value":"claude"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestSetProjectConfigMalformedBody(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doPostJSON(t, s, "/project/widgets/config", `{"field":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// Every field gets a row even when unset, because a row is the only place to
// edit its field: hiding empty ones would make an unset field unreachable.
func TestProjectDetailRendersAllConfigRows(t *testing.T) {
	store := db.NewMemStore()
	s := New(store, ":0").WithProjects(store)

	rec := doGet(t, s, "/project/widgets")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, field := range []string{"dir", "agent", "sandbox", "sandbox_profiles", "sandbox_env", "permission", "repo", "hooks", "env"} {
		if !strings.Contains(body, `data-row="`+field+`"`) {
			t.Errorf("row for unset field %q missing:\n%s", field, body)
		}
		if !strings.Contains(body, `data-field="`+field+`"`) {
			t.Errorf("edit button for field %q missing", field)
		}
	}
	if !strings.Contains(body, `data-editor="profiles"`) {
		t.Errorf("sandbox_profiles should use the profiles editor:\n%s", body)
	}
	if !strings.Contains(body, `data-editor="env"`) {
		t.Errorf("env should use the env editor")
	}
}

// Without a ProjectStore there is nothing to read or write, so the table and its
// edit affordances must not render at all.
func TestProjectDetailNoEditWithoutStore(t *testing.T) {
	s := New(db.NewMemStore(), ":0")

	rec := doGet(t, s, "/project/widgets")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// data-field only ever appears on an edit button (unlike the .cfg-edit CSS
	// rule, which is in the stylesheet regardless).
	if body := rec.Body.String(); strings.Contains(body, "data-field=") {
		t.Errorf("no edit buttons should render without a project store:\n%s", body)
	}
}

func TestProjectConfigRowsShowDefaultsForUnsetFallbacks(t *testing.T) {
	rows := projectConfigRows(db.ProjectRow{}, SessionDefaults{Agent: "claude", Sandbox: "bbwrap"})

	byField := map[string]configKV{}
	for _, r := range rows {
		byField[r.Field] = r
	}
	// agent and sandbox fall back to the global config, so an empty row names the
	// fallback instead of implying the setting is absent.
	if got := byField["agent"].Placeholder; got != "(default: claude)" {
		t.Errorf("agent placeholder = %q", got)
	}
	if got := byField["sandbox"].Placeholder; got != "(default: bbwrap)" {
		t.Errorf("sandbox placeholder = %q", got)
	}
	// The rest have no fallback.
	if got := byField["sandbox_profiles"].Placeholder; got != "(unset)" {
		t.Errorf("sandbox_profiles placeholder = %q", got)
	}
}

func TestProjectConfigRowsJoinsEnvLines(t *testing.T) {
	rows := projectConfigRows(db.ProjectRow{Env: []string{"A=1", "B=2"}}, SessionDefaults{})
	for _, r := range rows {
		if r.Field == "env" && r.Value != "A=1\nB=2" {
			t.Errorf("env value = %q, want newline-joined", r.Value)
		}
	}
}
