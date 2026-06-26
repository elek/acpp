package web

import (
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/elek/acpp/db"

	"github.com/labstack/echo/v4"
)

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

	data := map[string]interface{}{
		"Projects":        projects,
		"CurrentPage":     "projects",
		"ActiveProject":   activeProject,
		"ActiveDir":       activeDir,
		"ActiveSessionID": "",
		"ActiveSession":   nil,
		"Sessions":        nil,
		"CreatorEnabled":  s.creator != nil,
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
		Dir    string `json:"dir"`
		Prompt string `json:"prompt"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if body.Dir == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "dir is required"})
	}

	agent := s.defaults.Agent
	sandbox := s.defaults.Sandbox
	projectName := filepath.Base(body.Dir)
	if projectName == "" || projectName == "." || projectName == "/" {
		projectName = "default"
	}

	sessionID, err := s.creator.StartSessionWeb(body.Dir, agent, sandbox, "", projectName)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	// Send the first prompt if provided.
	if body.Prompt != "" && s.webChannel != nil {
		if err := s.webChannel.SubmitPrompt(sessionID, body.Prompt); err != nil {
			slog.Error("web: submit initial prompt", "session", sessionID, "error", err)
		}
	}

	return c.JSON(http.StatusCreated, map[string]string{"id": sessionID, "dir": filepath.Base(body.Dir)})
}
