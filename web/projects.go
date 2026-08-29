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
func (s *Server) resolveSessionDir(projectName, dir string) (string, bool) {
	if dir != "" {
		return dir, true
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

// configKV is a single key/value row in the project's configuration table.
type configKV struct {
	Key   string
	Value string
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

	// Configuration table: only populated when a ProjectStore is configured.
	// Blank fields are skipped so the table shows only what is actually set.
	var cfg []configKV
	if s.projects != nil {
		p, err := s.projects.GetProject(ctx, name)
		if err != nil {
			return err
		}
		add := func(k, v string) {
			if strings.TrimSpace(v) != "" {
				cfg = append(cfg, configKV{Key: k, Value: v})
			}
		}
		add("dir", p.Dir)
		add("agent", p.Agent)
		add("sandbox", p.Sandbox)
		add("sandbox_profiles", p.SandboxProfiles)
		add("permission", p.Permission)
		add("repo", p.Repo)
		add("hooks", p.Hooks)
		if len(p.Env) > 0 {
			add("env", strings.Join(p.Env, "\n"))
		}
	}

	return c.Render(http.StatusOK, "projectdetail.html", map[string]interface{}{
		"CurrentPage":    "projects",
		"ProjectName":    name,
		"Config":         cfg,
		"ActiveSessions": active,
		"ClosedSessions": closed,
	})
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
