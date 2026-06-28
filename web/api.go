package web

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/elek/acpp/db"
	"github.com/labstack/echo/v4"
)

// Version is the build version reported by GET /api/health. Override with -ldflags
// "-X github.com/elek/acpp/web.Version=...".
var Version = "dev"

// defaultContextWindow is used when the model's window is unknown.
const defaultContextWindow int64 = 200000

// gitTimeout bounds each best-effort git invocation in /api/projects so the
// endpoint stays responsive even with many projects.
const gitTimeout = 300 * time.Millisecond

// previewLen is the rune budget for truncated title/preview strings.
const previewLen = 120

// ProjectJSON is the JSON shape returned by GET /api/projects.
type ProjectJSON struct {
	Name         string `json:"name"`
	Dir          string `json:"dir"`
	Agent        string `json:"agent"`
	Branch       string `json:"branch"`
	Dirty        bool   `json:"dirty"`
	ChatCount    int    `json:"chat_count"`
	RunningCount int    `json:"running_count"`
}

// SessionJSON is the JSON shape returned by the session endpoints.
type SessionJSON struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Status        string   `json:"status"`
	StopReason    string   `json:"stop_reason"`
	Preview       string   `json:"preview"`
	Model         string   `json:"model"`
	ContextUsed   int64    `json:"context_used"`
	ContextWindow int64    `json:"context_window"`
	CostUSD       *float64 `json:"cost_usd"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// registerAPIRoutes wires the JSON API under /api. Existing HTML handlers are
// untouched; these endpoints serve the native clients (e.g. the Android app).
func (s *Server) registerAPIRoutes() {
	s.echo.GET("/api/health", s.apiHealth)
	s.echo.GET("/api/projects", s.apiProjects)
	s.echo.GET("/api/sessions", s.apiSessions)
	s.echo.GET("/api/session/:id", s.apiSession)
}

func (s *Server) apiHealth(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status":  "ok",
		"version": Version,
	})
}

func (s *Server) apiProjects(c echo.Context) error {
	ctx := c.Request().Context()

	// Mirror viewProjects: prefer the ProjectStore, fall back to dirs derived
	// from sessions.
	var base []db.ProjectListRow
	if s.projects != nil {
		var err error
		base, err = s.projects.ListProjects(ctx)
		if err != nil {
			return err
		}
	} else {
		dirs, err := s.store.ListProjectDirs(ctx)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			base = append(base, db.ProjectListRow{
				Name:       filepath.Base(d.Dir),
				Dir:        d.Dir,
				HasRunning: d.HasRunning,
			})
		}
	}

	// Allow callers to skip the per-project git lookups with ?git=0.
	wantGit := c.QueryParam("git") != "0"

	out := make([]ProjectJSON, 0, len(base))
	for _, p := range base {
		sessions, err := s.store.ListSessionsByProject(ctx, p.Name)
		if err != nil {
			return err
		}
		running := 0
		for _, sess := range sessions {
			if mapStatus(sess.Status) == "running" {
				running++
			}
		}
		pj := ProjectJSON{
			Name:         p.Name,
			Dir:          p.Dir,
			Agent:        p.Agent,
			ChatCount:    len(sessions),
			RunningCount: running,
		}
		if wantGit && p.Dir != "" {
			pj.Branch, pj.Dirty = gitInfo(p.Dir)
		}
		out = append(out, pj)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) apiSessions(c echo.Context) error {
	ctx := c.Request().Context()
	project := c.QueryParam("project")

	var rows []db.SessionRow
	var err error
	if project != "" {
		rows, err = s.store.ListSessionsByProject(ctx, project)
	} else {
		rows, err = s.store.ListSessions(ctx)
	}
	if err != nil {
		return err
	}

	out := make([]SessionJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.sessionJSON(ctx, r))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) apiSession(c echo.Context) error {
	ctx := c.Request().Context()
	id := c.Param("id")
	row, err := s.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s.sessionJSON(ctx, row))
}

// sessionJSON converts a SessionRow into the API shape, deriving title/preview
// cheaply from the prompt texts (no full log scan).
func (s *Server) sessionJSON(ctx context.Context, r db.SessionRow) SessionJSON {
	prompts, _ := s.store.GetPromptTexts(ctx, r.ID)
	var title, preview string
	if len(prompts) > 0 {
		title = truncate(prompts[0], previewLen)
		preview = truncate(prompts[len(prompts)-1], previewLen)
	}

	var cost *float64
	if r.CostUSD > 0 {
		c := r.CostUSD
		cost = &c
	}

	stopReason := ""
	if r.Status == "error" && r.ErrorMsg != "" {
		stopReason = r.ErrorMsg
	}

	updated := r.CreatedAt
	if r.FinishedAt != nil {
		updated = *r.FinishedAt
	}

	return SessionJSON{
		ID:            r.ID,
		Title:         title,
		Status:        mapStatus(r.Status),
		StopReason:    stopReason,
		Preview:       preview,
		Model:         r.Model,
		ContextUsed:   r.InputTokens + r.CacheCreationInputTokens + r.CacheReadInputTokens,
		ContextWindow: contextWindowForModel(r.Model),
		CostUSD:       cost,
		CreatedAt:     r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     updated.UTC().Format(time.RFC3339),
	}
}

// mapStatus collapses the DB session status into the four UI states.
func mapStatus(status string) string {
	switch status {
	case "running", "pending":
		return "running"
	case "complete":
		return "done"
	case "error":
		return "error"
	default:
		return "idle"
	}
}

// contextWindowForModel returns the model's context window, defaulting when the
// model is unknown.
func contextWindowForModel(model string) int64 {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "[1m]"), strings.Contains(m, "-1m"):
		return 1000000
	case strings.Contains(m, "claude"):
		return 200000
	case strings.Contains(m, "gpt-4o"), strings.Contains(m, "o1"), strings.Contains(m, "o3"):
		return 128000
	default:
		return defaultContextWindow
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// gitInfo returns the current branch and dirty flag for dir. It is best-effort:
// on any error (not a repo, git missing, timeout) it returns "", false.
func gitInfo(dir string) (branch string, dirty bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	b, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", false
	}
	branch = strings.TrimSpace(string(b))

	st, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return branch, false
	}
	dirty = strings.TrimSpace(string(st)) != ""
	return branch, dirty
}
