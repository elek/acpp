package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/hook"
	"github.com/elek/acpp/process"
	"github.com/elek/acpp/sandbox"

	"github.com/labstack/echo/v4"
)

// createProject creates (or, idempotently, confirms) a project row so it shows
// up in the sidebar and can host sessions. The directory is optional: when
// blank it is resolved from search_path by name, mirroring session start.
func (s *Server) createProject(c echo.Context) error {
	if s.projects == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "project creation not available"})
	}
	var body struct {
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	ctx := c.Request().Context()
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name is required"})
	}

	// Idempotent: an existing project is left untouched; the caller navigates to it.
	existing, err := s.projects.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range existing {
		if p.Name == name {
			return c.JSON(http.StatusOK, map[string]interface{}{"id": name, "existed": true})
		}
	}

	dir := strings.TrimSpace(body.Dir)
	if dir != "" {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "directory does not exist"})
		}
	} else if resolved, ok := config.FindProjectDir(s.searchPaths, name); ok {
		dir = resolved
	}

	if dir != "" {
		if err := s.projects.SetProjectField(ctx, name, "dir", dir); err != nil {
			return err
		}
	} else if _, err := s.projects.GetProject(ctx, name); err != nil {
		return err
	}

	return c.JSON(http.StatusCreated, map[string]string{"id": name})
}

// tabDotLimit caps how many session dots a single project tab renders, keeping
// the bottom bar compact even for projects with a long session history.
const tabDotLimit = 8

// projectTab is the bottom-bar view model for one project: the tab label plus a
// bounded set of recent sessions shown as status dots.
type projectTab struct {
	Name       string
	Dir        string
	Active     bool
	HasRunning bool
	Sessions   []projectTabDot
}

// projectTabDot is a single session rendered as a dot in a project tab.
type projectTabDot struct {
	ID     string
	Status string
	Title  string
	Active bool
}

// buildProjectTabs groups all sessions by project once and assembles the tab
// bar model, marking the active project and active session.
func (s *Server) buildProjectTabs(ctx context.Context, projects []db.ProjectListRow, activeProject, activeSessionID string) ([]projectTab, error) {
	allSessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	// ListSessions is ordered created_at DESC, so each project's slice is
	// already recent-first.
	byProject := make(map[string][]db.SessionRow, len(projects))
	for _, sess := range allSessions {
		byProject[sess.ProjectName] = append(byProject[sess.ProjectName], sess)
	}

	tabs := make([]projectTab, 0, len(projects))
	for _, p := range projects {
		tab := projectTab{
			Name:       p.Name,
			Dir:        p.Dir,
			Active:     p.Name == activeProject,
			HasRunning: p.HasRunning,
		}
		for _, sess := range byProject[p.Name] {
			// The bottom bar surfaces only live work: pending/running sessions.
			if sess.Status != "running" && sess.Status != "pending" {
				continue
			}
			if len(tab.Sessions) >= tabDotLimit {
				break
			}
			tab.Sessions = append(tab.Sessions, projectTabDot{
				ID:     sess.ID,
				Status: sess.Status,
				Title:  sess.CreatedAt.Format("Jan 2 15:04") + " — " + sess.Status,
				Active: tab.Active && sess.ID == activeSessionID,
			})
		}
		// Include a project only when it has at least one active session. The
		// active project always stays so navigating to it doesn't drop its tab.
		if len(tab.Sessions) == 0 && !tab.Active {
			continue
		}
		tabs = append(tabs, tab)
	}
	return tabs, nil
}

// listProjectRows returns the project rows used to build the tab bar, either
// from the ProjectStore or, when none is configured, synthesised from the
// distinct session directories.
func (s *Server) listProjectRows(ctx context.Context) ([]db.ProjectListRow, error) {
	if s.projects != nil {
		return s.projects.ListProjects(ctx)
	}
	dirs, err := s.store.ListProjectDirs(ctx)
	if err != nil {
		return nil, err
	}
	projects := make([]db.ProjectListRow, 0, len(dirs))
	for _, d := range dirs {
		projects = append(projects, db.ProjectListRow{
			Name:       filepath.Base(d.Dir),
			Dir:        d.Dir,
			HasRunning: d.HasRunning,
		})
	}
	return projects, nil
}

// viewTaskbar renders just the bottom tab bar's tabs as an HTML fragment,
// reusing buildProjectTabs so the live-updated bar can never drift from the
// server-rendered one. The /projects page fetches it on a lifecycle nudge,
// passing its current project/session so the active tab and dot stay marked for
// this browser's view.
func (s *Server) viewTaskbar(c echo.Context) error {
	ctx := c.Request().Context()
	projects, err := s.listProjectRows(ctx)
	if err != nil {
		return err
	}
	tabs, err := s.buildProjectTabs(ctx, projects, c.QueryParam("project"), c.QueryParam("session"))
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "taskbarTabs", map[string]interface{}{
		"ProjectTabs": tabs,
	})
}

// resolveSessionDir returns the working directory a new session for this
// project should run in: the explicit dir when one is known, otherwise the
// directory found by name in the search paths. The second result is false when
// neither is available.
//
// search_path is a list of directories on this machine, so a project on a
// remote location is only found through its stored dir.
func (s *Server) resolveSessionDir(projectName, dir string) (string, bool) {
	if dir != "" {
		return dir, true
	}
	if s.projects != nil {
		if p, err := s.projects.GetProject(context.Background(), projectName); err == nil && !process.IsLocal(p.Location) {
			return p.Dir, p.Dir != ""
		}
	}
	return config.FindProjectDir(s.searchPaths, projectName)
}

// startPendingSession creates a fresh, prompt-less session for the project so
// opening it lands on new work instead of finished history. It returns "" when
// no session could be started, in which case the caller falls back to the
// project's existing sessions. Unlike createProjectSession this never records a
// stillborn failed conversation: page loads are not an explicit user action, and
// an unresolvable directory would otherwise mint a new errored session on every
// visit.
func (s *Server) startPendingSession(projectName, dir string) string {
	if s.creator == nil {
		return ""
	}
	resolved, ok := s.resolveSessionDir(projectName, dir)
	if !ok {
		return ""
	}
	id, err := s.creator.StartSessionWeb(resolved, s.defaults.Agent, s.defaults.Sandbox, "", projectName)
	if err != nil {
		slog.Error("web: start pending session on project open", "project", projectName, "error", err)
		return ""
	}
	return id
}

func (s *Server) viewProjects(c echo.Context) error {
	ctx := c.Request().Context()

	projects, err := s.listProjectRows(ctx)
	if err != nil {
		return err
	}

	activeProject := c.QueryParam("project")
	activeSessionID := c.QueryParam("session")

	// Resolve the display name and dir for the active project.
	var activeDir string
	for _, p := range projects {
		if p.Name == activeProject {
			activeDir = p.Dir
			break
		}
	}

	// Resolve which of the project's sessions this page shows, before anything
	// else is built: when none is open the resolution starts a new session and
	// redirects, making the rest of the work here moot.
	var sessions []db.SessionRow
	if activeProject != "" {
		sessions, err = s.store.ListSessionsByProject(ctx, activeProject)
		if err != nil {
			return err
		}

		// If no session was specified, prefer an open one.
		if activeSessionID == "" {
			for _, sess := range sessions {
				if sess.Status == "running" || sess.Status == "pending" {
					activeSessionID = sess.ID
					break
				}
			}
		}

		// Nothing open: start a fresh pending session and land on that rather
		// than re-opening finished history. The new session is itself pending, so
		// the next open of this project picks it up above instead of stacking up
		// another one. Redirecting pins the id in the URL, keeping a reload from
		// starting yet another session.
		if activeSessionID == "" {
			if id := s.startPendingSession(activeProject, activeDir); id != "" {
				q := c.Request().URL.Query()
				q.Set("session", id)
				return c.Redirect(http.StatusSeeOther, "/projects?"+q.Encode())
			}
		}

		// No session could be started (session creation is unavailable or the
		// project's directory is unknown): fall back to the most recent session.
		if activeSessionID == "" && len(sessions) > 0 {
			activeSessionID = sessions[0].ID
		}
	}

	// Build the bottom tab bar model: one tab per project, each carrying a few
	// recent sessions rendered as status dots. A single ListSessions call is
	// grouped by project name to avoid a query per project.
	tabs, err := s.buildProjectTabs(ctx, projects, activeProject, activeSessionID)
	if err != nil {
		return err
	}

	data := map[string]interface{}{
		"Projects":        projects,
		"ProjectTabs":     tabs,
		"CurrentPage":     "projects",
		"ActiveProject":   activeProject,
		"ActiveDir":       activeDir,
		"ActiveSessionID": "",
		"ActiveSession":   nil,
		// Whether the active session's turn can still be stopped. Drives both the
		// initial Stop-button state and the client's isSessionRunning flag.
		"ActiveRunning":   false,
		"Sessions":        sessions,
		"CreatorEnabled":  s.creator != nil,
		"ProjectsEnabled": s.projects != nil,
		"Defaults":        s.defaults,
	}

	if activeSessionID != "" {
		data["ActiveSessionID"] = activeSessionID

		// Find the active session object
		for _, sess := range sessions {
			if sess.ID == activeSessionID {
				data["ActiveSession"] = &sess
				data["ActiveRunning"] = sess.Status == "running" || sess.Status == "pending"
				used := contextUsed(sess)
				window := contextWindow(sess)
				pct := 0
				if window > 0 {
					pct = int(used * 100 / window)
					if pct > 100 {
						pct = 100
					}
				}
				data["ContextUsed"] = used
				data["ContextWindow"] = window
				data["ContextPct"] = pct
				// The session row records the resolved sandbox type. Profiles
				// are not persisted per-session, so fall back to the project's
				// configured sandbox/profiles as the best available proxy.
				data["ActiveSandbox"] = sess.Sandbox
				if s.projects != nil {
					if p, err := s.projects.GetProject(ctx, activeProject); err == nil {
						data["ActiveProfiles"] = p.SandboxProfiles
						if sess.Sandbox == "" {
							data["ActiveSandbox"] = p.Sandbox
						}
					}
				}
				break
			}
		}
	}

	return c.Render(http.StatusOK, "projectview.html", data)
}

// sessionCard is the project-detail view model for one session, rendered as a
// card with a short prompt preview.
type sessionCard struct {
	ID      string
	Created string
	Prompt  string
	Status  string
	CostUSD float64
}

// configKV is a single editable row in the project's configuration table.
type configKV struct {
	// Key is the label shown to the user and Field the project column written by
	// the save endpoint. They are the same for every field except env, which is a
	// JSONB array rather than a text column.
	Key   string
	Field string
	Value string
	// Editor selects the popup's input widget: see projectConfigRows.
	Editor string
	// Placeholder is shown instead of an empty value, naming the fallback where
	// there is one so an empty row does not read as "no sandbox" or "no agent".
	Placeholder string
}

// Editor kinds. "text" is a single-line input; "profiles" and "hooks" are a
// checkbox list of the enumerated names plus a free-text field for anything not
// enumerated; "env" is a textarea of KEY=VALUE lines.
const (
	editorText     = "text"
	editorProfiles = "profiles"
	editorHooks    = "hooks"
	editorEnv      = "env"
)

// projectConfigRows builds the configuration table for a project. Every field is
// emitted even when unset: a row is the only place to edit its field, so hiding
// empty ones would make an unset field unreachable.
func projectConfigRows(p db.ProjectRow, defaults SessionDefaults) []configKV {
	unsetOr := func(fallback string) string {
		if fallback == "" {
			return "(unset)"
		}
		return "(default: " + fallback + ")"
	}
	return []configKV{
		{Key: "location", Field: "location", Value: p.Location, Editor: editorText, Placeholder: "(default: " + process.LocalName + ")"},
		{Key: "dir", Field: "dir", Value: p.Dir, Editor: editorText, Placeholder: "(unset)"},
		{Key: "agent", Field: "agent", Value: p.Agent, Editor: editorText, Placeholder: unsetOr(defaults.Agent)},
		{Key: "sandbox", Field: "sandbox", Value: p.Sandbox, Editor: editorText, Placeholder: unsetOr(defaults.Sandbox)},
		{Key: "sandbox_profiles", Field: "sandbox_profiles", Value: p.SandboxProfiles, Editor: editorProfiles, Placeholder: "(unset)"},
		{Key: "sandbox_env", Field: "sandbox_env", Value: p.SandboxEnv, Editor: editorText, Placeholder: "(default whitelist)"},
		{Key: "permission", Field: "permission", Value: p.Permission, Editor: editorText, Placeholder: "(unset)"},
		{Key: "repo", Field: "repo", Value: p.Repo, Editor: editorText, Placeholder: "(unset)"},
		{Key: "hooks", Field: "hooks", Value: p.Hooks, Editor: editorHooks, Placeholder: "(unset)"},
		{Key: "env", Field: "env", Value: strings.Join(p.Env, "\n"), Editor: editorEnv, Placeholder: "(unset)"},
	}
}

// firstPromptPreview returns a trimmed, length-capped preview of a session's
// first user prompt, or a placeholder when the session has none yet.
func (s *Server) firstPromptPreview(ctx context.Context, sessionID string) string {
	prompts, err := s.store.GetPromptTexts(ctx, sessionID)
	if err != nil {
		return ""
	}
	for _, p := range prompts {
		if t := strings.TrimSpace(p); t != "" {
			const max = 200
			if len(t) > max {
				t = t[:max] + "…"
			}
			return t
		}
	}
	return ""
}

// viewProjectDetail renders a project's overview page: its configuration plus
// its sessions grouped into active (running/pending) and closed cards. It is
// reached by clicking a project's name in the session view.
func (s *Server) viewProjectDetail(c echo.Context) error {
	ctx := c.Request().Context()
	name := c.Param("name")

	sessions, err := s.store.ListSessionsByProject(ctx, name)
	if err != nil {
		return err
	}

	var active, closed []sessionCard
	for _, sess := range sessions {
		card := sessionCard{
			ID:      sess.ID,
			Created: sess.CreatedAt.Format("Jan 2 15:04"),
			Prompt:  s.firstPromptPreview(ctx, sess.ID),
			Status:  sess.Status,
			CostUSD: sess.CostUSD,
		}
		if sess.Status == "running" || sess.Status == "pending" {
			active = append(active, card)
		} else {
			closed = append(closed, card)
		}
	}

	// Configuration table: only populated when a ProjectStore is configured,
	// since without one there is nothing to read or write.
	var cfg []configKV
	var profileNames []string
	if s.projects != nil {
		p, err := s.projects.GetProject(ctx, name)
		if err != nil {
			return err
		}
		cfg = projectConfigRows(p, s.defaults)

		// A failure to enumerate profiles must not take the page down: the popup
		// degrades to its free-text field, which can still express any value.
		profileNames, err = sandbox.ListProfiles()
		if err != nil {
			slog.Warn("could not list sandbox profiles for the config editor", "error", err)
			profileNames = nil
		}
	}

	return c.Render(http.StatusOK, "projectdetail.html", map[string]interface{}{
		"CurrentPage":    "projects",
		"ProjectName":    name,
		"Config":         cfg,
		"Editable":       s.projects != nil,
		"Profiles":       profileNames,
		"HookTypes":      hook.RegisteredTypes(),
		"ActiveSessions": active,
		"ClosedSessions": closed,
	})
}

// setProjectConfig writes one project config field, backing the edit popup on the
// project detail page. Unknown profile/hook names are accepted rather than
// rejected: the popup already warns about them, and rejecting would defeat its
// free-text escape hatch for values this server cannot enumerate. A genuinely bad
// value fails loudly at session creation, where sandbox and hook resolution
// report the offending name.
func (s *Server) setProjectConfig(c echo.Context) error {
	if s.projects == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "project configuration not available"})
	}
	name := c.Param("name")
	var body struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	ctx := c.Request().Context()

	// env is a JSONB array, not a text column, so it is written as a list of
	// non-blank lines rather than through SetProjectField.
	if body.Field == "env" {
		var entries []string
		for _, line := range strings.Split(body.Value, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				entries = append(entries, line)
			}
		}
		if err := s.projects.SetProjectEnv(ctx, name, entries); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]string{"value": strings.Join(entries, "\n")})
	}

	// SetProjectField already whitelists the writable columns, so an unknown
	// field surfaces as its error rather than a list duplicated here.
	value := strings.TrimSpace(body.Value)
	if err := s.projects.SetProjectField(ctx, name, body.Field, value); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"value": value})
}

func (s *Server) createProjectSession(c echo.Context) error {
	if s.creator == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "session creation not available"})
	}
	var body struct {
		Project string `json:"project"`
		Dir     string `json:"dir"`
		Prompt  string `json:"prompt"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	// A dir is not required: like Discord, resolve it from search_path by the
	// project name when unset. The project name is the explicit field if given,
	// else the base of the dir.
	dir := body.Dir
	projectName := body.Project
	if projectName == "" {
		projectName = filepath.Base(dir)
	}
	if projectName == "" || projectName == "." || projectName == "/" {
		projectName = "default"
	}

	if resolved, ok := s.resolveSessionDir(projectName, dir); ok {
		dir = resolved
	} else {
		// No directory could be found: record a stillborn conversation carrying
		// the failure so the frontend can open its window and show what happened.
		msg := fmt.Sprintf("Could not start a session: no directory named %q found in the search paths. Set the project's directory or add its parent to search_path.", projectName)
		if s.projects != nil {
			if p, err := s.projects.GetProject(c.Request().Context(), projectName); err == nil && !process.IsLocal(p.Location) {
				msg = fmt.Sprintf("Could not start a session: project %q runs on location %q, which needs the project's directory set (search_path only covers this machine).", projectName, p.Location)
			}
		}
		id := s.creator.StartFailedSessionWeb(dir, projectName, msg)
		return c.JSON(http.StatusCreated, map[string]string{"id": id, "dir": "", "failed": "true"})
	}

	sessionID, err := s.creator.StartSessionWeb(dir, s.defaults.Agent, s.defaults.Sandbox, "", projectName)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	// Send the first prompt if provided. The new-session modal is text-only;
	// image paste happens in the live prompt bar.
	if body.Prompt != "" && s.webChannel != nil {
		if err := s.webChannel.SubmitPrompt(sessionID, body.Prompt, nil); err != nil {
			slog.Error("web: submit initial prompt", "session", sessionID, "error", err)
		}
	}

	return c.JSON(http.StatusCreated, map[string]string{"id": sessionID, "dir": filepath.Base(dir)})
}
