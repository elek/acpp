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
		for i, sess := range byProject[p.Name] {
			if i >= tabDotLimit {
				break
			}
			tab.Sessions = append(tab.Sessions, projectTabDot{
				ID:     sess.ID,
				Status: sess.Status,
				Title:  sess.CreatedAt.Format("Jan 2 15:04") + " — " + sess.Status,
				Active: tab.Active && sess.ID == activeSessionID,
			})
		}
		tabs = append(tabs, tab)
	}
	return tabs, nil
}

func (s *Server) viewProjects(c echo.Context) error {
	ctx := c.Request().Context()

	var projects []db.ProjectListRow
	if s.projects != nil {
		var err error
		projects, err = s.projects.ListProjects(ctx)
		if err != nil {
			return err
		}
	} else {
		dirs, err := s.store.ListProjectDirs(ctx)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			projects = append(projects, db.ProjectListRow{
				Name:       filepath.Base(d.Dir),
				Dir:        d.Dir,
				HasRunning: d.HasRunning,
			})
		}
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
		"Sessions":        nil,
		"CreatorEnabled":  s.creator != nil,
		"ProjectsEnabled": s.projects != nil,
		"Defaults":        s.defaults,
	}

	if activeProject != "" {
		sessions, err := s.store.ListSessionsByProject(ctx, activeProject)
		if err != nil {
			return err
		}
		data["Sessions"] = sessions

		if len(sessions) > 0 {
			// If no session specified, pick the latest running or the most recent one
			if activeSessionID == "" {
				// Prefer a running session
				for _, sess := range sessions {
					if sess.Status == "running" || sess.Status == "pending" {
						activeSessionID = sess.ID
						break
					}
				}
				// Otherwise pick the most recent
				if activeSessionID == "" {
					activeSessionID = sessions[0].ID
				}
			}
			data["ActiveSessionID"] = activeSessionID

			// Find the active session object
			for _, sess := range sessions {
				if sess.ID == activeSessionID {
					data["ActiveSession"] = &sess
					break
				}
			}
		}
	}

	return c.Render(http.StatusOK, "projectview.html", data)
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

	if dir == "" {
		if resolved, ok := config.FindProjectDir(s.searchPaths, projectName); ok {
			dir = resolved
		} else {
			// No directory could be found: record a stillborn conversation carrying
			// the failure so the frontend can open its window and show what happened.
			msg := fmt.Sprintf("Could not start a session: no directory named %q found in the search paths. Set the project's directory or add its parent to search_path.", projectName)
			id := s.creator.StartFailedSessionWeb(dir, projectName, msg)
			return c.JSON(http.StatusCreated, map[string]string{"id": id, "dir": "", "failed": "true"})
		}
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
