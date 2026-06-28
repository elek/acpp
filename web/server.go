package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/elek/acpp/db"
	"github.com/elek/acpp/router"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// logEntry is the JSON shape returned by the events API.
type logEntry struct {
	ID        int64           `json:"id,omitempty"`
	Time      string          `json:"time"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

//go:embed templates/*.html
var templateFS embed.FS

type tmplRenderer struct {
	templates *template.Template
}

func (t *tmplRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

// SessionCloser can close a running session by ID.
type SessionCloser interface {
	CloseSession(sessionID string)
}

// SessionCreator can create a new session and return its ID.
type SessionCreator interface {
	StartSessionWeb(dir string, agent string, sandbox string, sandboxProfiles string, projectName string) (string, error)
}

// Server is the web UI server.
type Server struct {
	store      db.SessionReader
	closer     SessionCloser
	creator    SessionCreator
	projects   db.ProjectStore
	defaults   SessionDefaults
	echo       *echo.Echo
	addr       string
	hub        *Hub
	webChannel *WebChannel
	upgrader   websocket.Upgrader
}

// SessionDefaults holds default values shown in the new-session form.
type SessionDefaults struct {
	Agent   string
	Dir     string
	Sandbox string
}

// New creates a web server with routes configured.
func New(store db.SessionReader, addr string) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	t, _ := template.New("").Funcs(template.FuncMap{
		"bytesToString": func(b []byte) string { return string(b) },
		"basename":      func(path string) string { return filepath.Base(path) },
		"plusOne":       func(i int) int { return i + 1 },
		"formatDurationMs": func(ms int64) string {
			if ms == 0 {
				return "-"
			}
			d := time.Duration(ms) * time.Millisecond
			if d < time.Minute {
				return fmt.Sprintf("%.0fs", d.Seconds())
			}
			m := int(d.Minutes())
			s := int(d.Seconds()) % 60
			if m < 60 {
				return fmt.Sprintf("%dm%ds", m, s)
			}
			h := m / 60
			m = m % 60
			return fmt.Sprintf("%dh%dm", h, m)
		},
		"divf": func(a, b float64) float64 { return a / b },
		"addf": func(a, b float64) float64 { return a + b },
		"itof": func(v int64) float64 { return float64(v) },
		"millions": func(v int64) string {
			if v == 0 {
				return "0"
			}
			m := float64(v) / 1_000_000
			if m >= 10 {
				return fmt.Sprintf("%.0fM", m)
			}
			if m >= 1 {
				return fmt.Sprintf("%.1fM", m)
			}
			k := float64(v) / 1_000
			if k >= 10 {
				return fmt.Sprintf("%.0fk", k)
			}
			if k >= 1 {
				return fmt.Sprintf("%.1fk", k)
			}
			return fmt.Sprintf("%d", v)
		},
	}).ParseFS(templateFS, "templates/*.html")
	e.Renderer = &tmplRenderer{templates: t}

	hub := NewHub()
	s := &Server{
		store: store,
		echo:  e,
		addr:  addr,
		hub:   hub,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
	e.GET("/", func(c echo.Context) error { return c.Redirect(http.StatusFound, "/projects") })
	e.GET("/sessions", s.listSessions)
	e.GET("/projects", s.viewProjects)
	e.POST("/projects/session", s.createProjectSession)
	e.GET("/session/:id", s.viewSession)
	e.GET("/session/:id/logs", s.viewSessionLogs)
	e.GET("/session/:id/tool/:toolCallId", s.viewToolCall)
	e.GET("/session/:id/events", s.sessionEvents)
	e.GET("/session/:id/ws", s.sessionWebSocket)
	e.POST("/session", s.createSession)
	e.POST("/session/:id/stop", s.stopSession)
	e.POST("/session/:id/prompt", s.sendPrompt)
	e.GET("/compare/:commit", s.viewCompare)
	e.GET("/stats", s.viewStats)
	e.GET("/stats/projects", s.viewProjectStats)
	e.GET("/stats/models", s.viewModelStats)
	e.GET("/stats/insights", s.viewInsights)
	s.registerAPIRoutes()
	return s
}

// WithRouter wires the web UI to a router for live sessions. It creates a
// WebChannel subscriber (which streams every conversation's updates to connected
// browsers) and registers it as both the session creator and closer. Persistence
// is wired separately via the persistence package. Returns the server for
// chaining.
func (s *Server) WithRouter(rt *router.Router) *Server {
	s.webChannel = NewWebChannel(rt, s.hub)
	s.creator = s.webChannel
	s.closer = s.webChannel
	return s
}

// WebChannel returns the router-backed channel used for live sessions, or nil if
// WithRouter was not called.
func (s *Server) WebChannel() *WebChannel {
	return s.webChannel
}

// WithCloser sets a SessionCloser that allows the web UI to stop running sessions.
func (s *Server) WithCloser(closer SessionCloser) *Server {
	s.closer = closer
	return s
}

// WithCreator sets a SessionCreator that allows the web UI to create new sessions.
func (s *Server) WithCreator(creator SessionCreator) *Server {
	s.creator = creator
	return s
}

// WithProjects sets a ProjectStore for listing persisted projects.
func (s *Server) WithProjects(projects db.ProjectStore) *Server {
	s.projects = projects
	return s
}

// WithDefaults sets the default values shown in the new-session form.
func (s *Server) WithDefaults(defaults SessionDefaults) *Server {
	s.defaults = defaults
	return s
}

// Start runs the server until the context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.echo.Close()
	}()
	err := s.echo.Start(s.addr)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) listSessions(c echo.Context) error {
	sessions, err := s.store.ListSessions(c.Request().Context())
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "list.html", map[string]interface{}{
		"Sessions":       sessions,
		"CurrentPage":    "sessions",
		"Defaults":       s.defaults,
		"CreatorEnabled": s.creator != nil,
	})
}

func (s *Server) createSession(c echo.Context) error {
	if s.creator == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "session creation not available"})
	}
	var body struct {
		Dir             string `json:"dir"`
		Agent           string `json:"agent"`
		Sandbox         string `json:"sandbox"`
		SandboxProfiles string `json:"sandbox_profiles"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if body.Agent == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "agent is required"})
	}
	if body.Dir == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "dir is required"})
	}
	projectName := filepath.Base(body.Dir)
	if projectName == "" || projectName == "." || projectName == "/" {
		projectName = "default"
	}
	sessionID, err := s.creator.StartSessionWeb(body.Dir, body.Agent, body.Sandbox, body.SandboxProfiles, projectName)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]string{"id": sessionID})
}

func (s *Server) viewSession(c echo.Context) error {
	id := c.Param("id")
	session, err := s.store.GetSession(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "session.html", map[string]interface{}{
		"Session":     session,
		"CurrentPage": "session",
	})
}

func (s *Server) viewSessionLogs(c echo.Context) error {
	id := c.Param("id")
	session, err := s.store.GetSession(c.Request().Context(), id)
	if err != nil {
		return err
	}
	logs, err := s.store.GetSessionLogs(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "logs.html", map[string]interface{}{
		"Session":     session,
		"Logs":        logs,
		"CurrentPage": "logs",
	})
}

func (s *Server) viewToolCall(c echo.Context) error {
	sessionID := c.Param("id")
	toolCallID := c.Param("toolCallId")
	if toolCallID == "" {
		return c.String(http.StatusBadRequest, "missing toolCallId")
	}
	session, err := s.store.GetSession(c.Request().Context(), sessionID)
	if err != nil {
		return err
	}
	logs, err := s.store.GetLogsByToolCallID(c.Request().Context(), sessionID, toolCallID)
	if err != nil {
		return err
	}
	if len(logs) == 0 {
		return c.String(http.StatusNotFound, "no log entries found for this tool call")
	}
	return c.Render(http.StatusOK, "tool.html", map[string]interface{}{
		"Session":     session,
		"Logs":        logs,
		"ToolCallID":  toolCallID,
		"CurrentPage": "session",
	})
}

func (s *Server) sessionEvents(c echo.Context) error {
	id := c.Param("id")
	logs, err := s.store.GetSessionLogs(c.Request().Context(), id)
	if err != nil {
		return err
	}
	entries := make([]logEntry, len(logs))
	for i, l := range logs {
		entries[i] = logEntry{
			ID:        l.ID,
			Time:      l.CreatedAt.Format("15:04:05.000"),
			EventType: l.EventType,
			Payload:   l.Payload,
		}
	}
	return c.JSON(http.StatusOK, entries)
}

func (s *Server) stopSession(c echo.Context) error {
	if s.closer == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "session management not available"})
	}
	id := c.Param("id")
	s.closer.CloseSession(id)
	if ref := c.Request().Referer(); ref != "" {
		return c.Redirect(http.StatusSeeOther, ref)
	}
	return c.Redirect(http.StatusSeeOther, "/session/"+id)
}

func (s *Server) sendPrompt(c echo.Context) error {
	id := c.Param("id")
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if body.Prompt == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "prompt is required"})
	}
	if s.webChannel == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "live sessions not available"})
	}
	// Leading-slash messages (e.g. /clear, /cancel) are recognised as commands
	// by the router itself, so the prompt is forwarded verbatim.
	if err := s.webChannel.SubmitPrompt(id, body.Prompt); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) viewCompare(c echo.Context) error {
	commit := c.Param("commit")
	sessions, err := s.store.GetSessionsByCommit(c.Request().Context(), commit)
	if err != nil {
		return err
	}

	type compareSession struct {
		Session db.SessionRow
		Prompts []string
	}

	var items []compareSession
	for _, sess := range sessions {
		prompts, err := s.store.GetPromptTexts(c.Request().Context(), sess.ID)
		if err != nil {
			return err
		}
		items = append(items, compareSession{Session: sess, Prompts: prompts})
	}

	return c.Render(http.StatusOK, "compare.html", map[string]interface{}{
		"Commit":      commit,
		"Items":       items,
		"CurrentPage": "sessions",
	})
}

func (s *Server) sessionWebSocket(c echo.Context) error {
	id := c.Param("id")
	ws, err := s.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	sub := s.hub.Subscribe(id)
	defer s.hub.Unsubscribe(id, sub)

	// Read pump: discard incoming messages, detect close.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case msg, ok := <-sub.ch:
			if !ok {
				return nil
			}
			if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return nil
			}
		case <-done:
			return nil
		}
	}
}

func (s *Server) viewStats(c echo.Context) error {
	agent := c.QueryParam("agent")
	ctx := c.Request().Context()

	since := time.Now().AddDate(0, 0, -29)
	if q := c.QueryParam("since"); q != "" {
		t, err := time.Parse(time.RFC3339, q)
		if err != nil {
			// Try date-only format as fallback.
			t, err = time.Parse("2006-01-02", q)
			if err != nil {
				return c.String(http.StatusBadRequest, "invalid since parameter, expected RFC3339 (e.g. 2026-01-03T00:00:00Z) or date (2026-01-03)")
			}
		}
		since = t
	}

	stats, err := s.store.GetDailyStats(ctx, since, agent)
	if err != nil {
		return err
	}

	agents, err := s.store.GetDistinctAgents(ctx)
	if err != nil {
		return err
	}

	// Compute totals.
	var totals db.DailyStatsRow
	for _, d := range stats {
		totals.Sessions += d.Sessions
		totals.PromptCount += d.PromptCount
		totals.InputTokens += d.InputTokens
		totals.OutputTokens += d.OutputTokens
		totals.CacheCreationInputTokens += d.CacheCreationInputTokens
		totals.CacheReadInputTokens += d.CacheReadInputTokens
		totals.CostUSD += d.CostUSD
	}

	return c.Render(http.StatusOK, "stats.html", map[string]interface{}{
		"Stats":       stats,
		"Totals":      totals,
		"CurrentPage": "stats",
		"StatsView":   "daily",
		"Since":       since.Format("2006-01-02"),
		"Agents":      agents,
		"ActiveAgent": agent,
	})
}

func (s *Server) viewProjectStats(c echo.Context) error {
	agent := c.QueryParam("agent")
	ctx := c.Request().Context()

	stats, err := s.store.GetMonthlyStatsByDir(ctx, 6, agent)
	if err != nil {
		return err
	}

	agents, err := s.store.GetDistinctAgents(ctx)
	if err != nil {
		return err
	}

	// Group rows by month, compute per-month totals.
	type monthGroup struct {
		Month string
		Rows  []db.MonthlyDirStatsRow
		Total db.MonthlyDirStatsRow
	}
	var months []monthGroup
	idx := map[string]int{}
	for _, r := range stats {
		i, ok := idx[r.Month]
		if !ok {
			i = len(months)
			idx[r.Month] = i
			months = append(months, monthGroup{Month: r.Month})
		}
		months[i].Rows = append(months[i].Rows, r)
		months[i].Total.Sessions += r.Sessions
		months[i].Total.PromptCount += r.PromptCount
		months[i].Total.InputTokens += r.InputTokens
		months[i].Total.OutputTokens += r.OutputTokens
		months[i].Total.CacheCreationInputTokens += r.CacheCreationInputTokens
		months[i].Total.CacheReadInputTokens += r.CacheReadInputTokens
		months[i].Total.CostUSD += r.CostUSD
	}

	return c.Render(http.StatusOK, "projects.html", map[string]interface{}{
		"Months":      months,
		"CurrentPage": "stats",
		"StatsView":   "projects",
		"Agents":      agents,
		"ActiveAgent": agent,
	})
}

func (s *Server) viewModelStats(c echo.Context) error {
	ctx := c.Request().Context()

	since := time.Now().AddDate(0, 0, -29)
	if q := c.QueryParam("since"); q != "" {
		t, err := time.Parse(time.RFC3339, q)
		if err != nil {
			t, err = time.Parse("2006-01-02", q)
			if err != nil {
				return c.String(http.StatusBadRequest, "invalid since parameter")
			}
		}
		since = t
	}

	stats, err := s.store.GetModelStats(ctx, since)
	if err != nil {
		return err
	}

	// Compute totals.
	var totals db.ModelStatsRow
	for _, r := range stats {
		totals.Sessions += r.Sessions
		totals.PromptCount += r.PromptCount
		totals.InputTokens += r.InputTokens
		totals.OutputTokens += r.OutputTokens
		totals.CacheCreationInputTokens += r.CacheCreationInputTokens
		totals.CacheReadInputTokens += r.CacheReadInputTokens
		totals.CostUSD += r.CostUSD
		totals.PromptDurationMs += r.PromptDurationMs
	}

	return c.Render(http.StatusOK, "models.html", map[string]interface{}{
		"Stats":       stats,
		"Totals":      totals,
		"CurrentPage": "stats",
		"StatsView":   "models",
		"Since":       since.Format("2006-01-02"),
	})
}

func (s *Server) viewInsights(c echo.Context) error {
	since := time.Now().AddDate(0, 0, -29)
	if q := c.QueryParam("since"); q != "" {
		t, err := time.Parse(time.RFC3339, q)
		if err != nil {
			t, err = time.Parse("2006-01-02", q)
			if err != nil {
				return c.String(http.StatusBadRequest, "invalid since parameter, expected RFC3339 (e.g. 2026-01-03T00:00:00Z) or date (2026-01-03)")
			}
		}
		since = t
	}

	data, err := s.store.GetInsights(c.Request().Context(), since)
	if err != nil {
		return err
	}

	return c.Render(http.StatusOK, "insights.html", map[string]interface{}{
		"Data":        data,
		"CurrentPage": "stats",
		"StatsView":   "insights",
		"Since":       since.Format("2006-01-02"),
	})
}
