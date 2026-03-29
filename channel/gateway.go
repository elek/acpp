package channel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
	acp2 "github.com/elek/acpp/acp"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// IdleTimeout is the duration of inactivity after which a session is automatically closed.
const IdleTimeout = 24 * time.Hour

// EventPublisher receives log events for live streaming (e.g., WebSocket).
type EventPublisher interface {
	PublishEvent(sessionID, eventType string, payload json.RawMessage)
}

// Gateway is the central router that handles incoming messages from channels
// and forwards them to sessions, and handles incoming updates from sessions
// and forwards them to subscribed channel endpoints.
//
// It supports many-to-many relationships between sessions and channel endpoints.
type Gateway struct {
	sessions  *acp2.SessionManager
	channels  *ChannelManager
	config    *config.Config
	store     db.SessionWriter
	projects  db.ProjectStore
	publisher EventPublisher
	node      string // hostname identifying where agents run

	mu sync.RWMutex
	// Many-to-many routing maps
	sessionToSources map[string][]ChannelSource // sessionID -> subscribed endpoints
	sourceToSessions map[ChannelSource][]string // endpoint -> subscribed sessionIDs
	activeSession    map[ChannelSource]string   // endpoint -> active sessionID (for prompt routing)

	// Per-session relay (handles ACP->Channel translation)
	relays map[string]*Relay // sessionID -> Relay

	// Per-session hooks (lifecycle callbacks)
	hooks map[string][]Hook // sessionID -> hooks

	// Per-session project name (for web session /clear to recreate with same project)
	sessionProjects map[string]string // sessionID -> projectName

	// Per-endpoint state
	lastActivity sync.Map // ChannelSource -> time.Time
	dirOverrides sync.Map // ChannelSource -> string

	// Tracks all goroutines dispatched for input handling so they can be
	// awaited during shutdown.
	handlers sync.WaitGroup
}

// NewGateway creates a new Gateway.
func NewGateway(cfg *config.Config, store db.SessionWriter, projects db.ProjectStore, sessions *acp2.SessionManager, channels *ChannelManager) *Gateway {
	node, _ := os.Hostname()
	return &Gateway{
		sessions:         sessions,
		channels:         channels,
		config:           cfg,
		store:            store,
		projects:         projects,
		node:             node,
		sessionToSources: make(map[string][]ChannelSource),
		sourceToSessions: make(map[ChannelSource][]string),
		activeSession:    make(map[ChannelSource]string),
		relays:           make(map[string]*Relay),
		hooks:            make(map[string][]Hook),
		sessionProjects:  make(map[string]string),
	}
}

// WithPublisher sets an EventPublisher for live-streaming log events.
func (gw *Gateway) WithPublisher(p EventPublisher) {
	gw.publisher = p
}

// getHooks returns the hooks for a session.
func (gw *Gateway) getHooks(sessionID string) []Hook {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	return gw.hooks[sessionID]
}

// resolveHooks parses a comma-separated hooks config string and returns
// hook instances from the global HookRegistry. Unknown names are logged and skipped.
func resolveHooks(hooksConfig string) []Hook {
	if hooksConfig == "" {
		return nil
	}
	var hooks []Hook
	for _, name := range strings.Split(hooksConfig, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		factory, ok := HookRegistry[name]
		if !ok {
			slog.Warn("unknown hook, skipping", "hook", name)
			continue
		}
		hooks = append(hooks, factory())
	}
	return hooks
}

// Start creates all channels via the ChannelManager, begins consuming their input,
// and starts background tasks.
func (gw *Gateway) Start(ctx context.Context) error {
	// Mark any sessions left running from a previous process as completed.
	if n, err := gw.store.CompleteRunningSessions(ctx); err != nil {
		slog.Error("failed to complete stale sessions", "err", err)
	} else if n > 0 {
		slog.Info("completed stale sessions from previous run", "count", n)
	}

	if err := gw.channels.Start(ctx); err != nil {
		return err
	}

	for chID, ch := range gw.channels.All() {
		gw.startChannelListener(ctx, chID, ch)
	}
	gw.startIdleChecker(ctx)
	return nil
}

// startChannelListener launches a goroutine that reads from a channel's Input()
// and dispatches commands and messages. Each input is handled in its own goroutine
// so that long-running prompts don't block shell commands or slash commands.
// All dispatched goroutines are tracked via gw.handlers for clean shutdown.
func (gw *Gateway) startChannelListener(ctx context.Context, chID ChannelID, ch Channel) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case input, ok := <-ch.Input():
				if !ok {
					return
				}
				gw.handlers.Add(1)
				go func() {
					defer gw.handlers.Done()
					cs := ChannelSource{ChannelID: chID, SourceID: input.Source}
					var err error
					if input.Command != "" {
						err = gw.OnCommandReceived(cs, input.Command)
					} else {
						err = gw.OnMessageReceived(cs, input.Message, input.Images)
					}
					if err != nil {
						slog.Error("failed to handle input", "channel", chID, "source", input.Source, "err", err)
					}
				}()
			}
		}
	}()
}

// Wait blocks until all dispatched input handler goroutines have finished.
// Call this during shutdown after cancelling the context passed to Start.
func (gw *Gateway) Wait() {
	gw.handlers.Wait()
}

// startIdleChecker launches a background goroutine that periodically checks for
// sessions with no prompt activity for IdleTimeout and closes them.
func (gw *Gateway) startIdleChecker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(IdleTimeout)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				gw.closeIdleSessions()
			}
		}
	}()
}

// Subscribe links a session to a channel endpoint.
// If it's the first session for that endpoint, it becomes the active session.
func (gw *Gateway) Subscribe(sessionID string, cs ChannelSource) {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	gw.sessionToSources[sessionID] = append(gw.sessionToSources[sessionID], cs)
	gw.sourceToSessions[cs] = append(gw.sourceToSessions[cs], sessionID)

	// First session for this endpoint becomes active
	if _, ok := gw.activeSession[cs]; !ok {
		gw.activeSession[cs] = sessionID
	}
}

// Unsubscribe removes the link between a session and a channel endpoint.
// If the active session is removed, picks another or clears.
func (gw *Gateway) Unsubscribe(sessionID string, cs ChannelSource) {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	gw.sessionToSources[sessionID] = removeString(gw.sessionToSources[sessionID], cs)
	if len(gw.sessionToSources[sessionID]) == 0 {
		delete(gw.sessionToSources, sessionID)
	}

	gw.sourceToSessions[cs] = removeStringFromSlice(gw.sourceToSessions[cs], sessionID)
	if len(gw.sourceToSessions[cs]) == 0 {
		delete(gw.sourceToSessions, cs)
		delete(gw.activeSession, cs)
	} else if gw.activeSession[cs] == sessionID {
		// Pick another session as active
		gw.activeSession[cs] = gw.sourceToSessions[cs][0]
	}
}

// SetActiveSession sets the active session for a channel endpoint.
func (gw *Gateway) SetActiveSession(cs ChannelSource, sessionID string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.activeSession[cs] = sessionID
}

// StartSession creates a new session, relay, subscribes the source, and persists to DB.
func (gw *Gateway) StartSession(cs ChannelSource, dir string, agent string, sandbox string, permission string, projectName string, env []string, hooks string) (string, error) {
	ch, ok := gw.channels.Get(cs.ChannelID)
	if !ok {
		return "", fmt.Errorf("channel %s not found", cs.ChannelID)
	}

	// Get channel name for logging
	sourceName := string(cs.SourceID)
	if name, err := ch.GetChannelName(cs.SourceID); err == nil && name != "" {
		sourceName = name
	}

	sessionID := uuid.NewString()
	createdAt := time.Now()

	// Build permission handler from config rules + channel's AskYesNo
	askPermission := acp2.AskPermission(func(question string) (bool, error) {
		return ch.AskYesNo(cs.SourceID, question)
	})
	var permHandler acp2.PermissionHandler
	if strings.EqualFold(permission, "manual") {
		permHandler = acp2.AskAll(askPermission)
	} else {
		permHandler = acp2.NewPermissionHandler(gw.config, askPermission)
	}

	opts := acp2.SessionOpts{
		Source:            sourceName,
		Agent:             agent,
		CWD:               dir,
		Sandbox:           sandbox,
		Env:               env,
		PermissionHandler: permHandler,
	}

	sess, err := gw.sessions.Create(sessionID, opts)
	if err != nil {
		return "", errors.WithStack(err)
	}

	// Create the onUpdate callback that persists events to the database
	// and publishes to live WebSocket subscribers.
	onUpdate := func(event acp2.SessionEvent) {
		raw, eventType := marshalEvent(event.Update)
		if err := gw.store.InsertLog(context.Background(), sessionID, eventType, raw); err != nil {
			slog.Error("failed to insert log", "session", sessionID, "err", err)
		}
		if gw.publisher != nil {
			gw.publisher.PublishEvent(sessionID, eventType, raw)
		}
	}

	// Create broadcast function for this session
	broadcast := gw.makeBroadcast(sessionID)

	relay := NewRelay(sess, broadcast, onUpdate)

	gw.mu.Lock()
	gw.relays[sessionID] = relay
	gw.mu.Unlock()

	gw.Subscribe(sessionID, cs)
	gw.TouchActivity(cs)

	// Detect git commit hash for the working directory
	gitCommit := detectGitCommit(dir)

	// Persist session to database
	if err := gw.store.InsertSession(context.Background(), sessionID, sourceName, agent, dir, sandbox, gw.node, gitCommit, projectName, env, createdAt); err != nil {
		slog.Error("failed to insert session", "session", sessionID, "err", err)
	}

	// Instantiate per-session hooks from project config
	if sessionHooks := resolveHooks(hooks); len(sessionHooks) > 0 {
		gw.mu.Lock()
		gw.hooks[sessionID] = sessionHooks
		gw.mu.Unlock()
		for _, hook := range sessionHooks {
			hook.OnSessionStarted(dir)
		}
	}

	return sessionID, nil
}

// StartSessionWeb creates a new session from the web UI without a channel source.
// The session is accessible via its ID for prompts and live streaming.
func (gw *Gateway) StartSessionWeb(dir string, agent string, sandbox string, projectName string) (string, error) {
	sessionID := uuid.NewString()
	createdAt := time.Now()

	// Web sessions have no interactive channel, so askPermission is nil.
	// Rules with "deny" still apply; "ask" rules will also deny (no user to prompt).
	permHandler := acp2.NewPermissionHandler(gw.config, nil)

	opts := acp2.SessionOpts{
		Source:            "web",
		Agent:             agent,
		CWD:               dir,
		Sandbox:           sandbox,
		PermissionHandler: permHandler,
	}

	sess, err := gw.sessions.Create(sessionID, opts)
	if err != nil {
		return "", errors.WithStack(err)
	}

	onUpdate := func(event acp2.SessionEvent) {
		raw, eventType := marshalEvent(event.Update)
		if err := gw.store.InsertLog(context.Background(), sessionID, eventType, raw); err != nil {
			slog.Error("failed to insert log", "session", sessionID, "err", err)
		}
		if gw.publisher != nil {
			gw.publisher.PublishEvent(sessionID, eventType, raw)
		}
	}

	// No channel broadcast for web sessions — updates go through WebSocket only.
	noop := func(fn func(Channel, SourceID)) {}
	relay := NewRelay(sess, noop, onUpdate)

	gw.mu.Lock()
	gw.relays[sessionID] = relay
	gw.sessionProjects[sessionID] = projectName
	gw.mu.Unlock()

	// Detect git commit hash for the working directory
	gitCommit := detectGitCommit(dir)

	if err := gw.store.InsertSession(context.Background(), sessionID, "web", agent, dir, sandbox, gw.node, gitCommit, projectName, nil, createdAt); err != nil {
		slog.Error("failed to insert session", "session", sessionID, "err", err)
	}

	// Instantiate per-session hooks from project config
	proj, projErr := gw.projects.GetProject(context.Background(), projectName)
	if projErr != nil {
		slog.Warn("failed to get project for hooks", "project", projectName, "err", projErr)
	}
	if sessionHooks := resolveHooks(proj.Hooks); len(sessionHooks) > 0 {
		gw.mu.Lock()
		gw.hooks[sessionID] = sessionHooks
		gw.mu.Unlock()
		for _, hook := range sessionHooks {
			hook.OnSessionStarted(dir)
		}
	}

	return sessionID, nil
}

// CloseSession unsubscribes from all endpoints, closes the session, and persists to DB.
func (gw *Gateway) CloseSession(sessionID string) {
	gw.mu.Lock()
	relay, ok := gw.relays[sessionID]
	sources := append([]ChannelSource{}, gw.sessionToSources[sessionID]...)
	gw.mu.Unlock()

	if !ok {
		return
	}

	// Persist final state
	info := relay.Session().GetStatusInfo()
	sessionError := relay.Session().GetError()
	if info.Status == acp2.StatusRunning || info.Status == acp2.StatusPending {
		info.Status = acp2.StatusComplete
	}
	if err := gw.store.FinishSession(context.Background(), sessionID, info, sessionError); err != nil {
		slog.Error("failed to finish session in db", "session", sessionID, "err", err)
	}

	// Unsubscribe from all endpoints
	for _, cs := range sources {
		gw.Unsubscribe(sessionID, cs)
		gw.lastActivity.Delete(cs)
	}

	// Close via session manager
	gw.sessions.Close(sessionID)

	gw.mu.Lock()
	delete(gw.relays, sessionID)
	delete(gw.hooks, sessionID)
	delete(gw.sessionProjects, sessionID)
	gw.mu.Unlock()
}

// GetRelay returns the relay for the active session on a channel endpoint.
func (gw *Gateway) GetRelay(cs ChannelSource) (*Relay, bool) {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	sessionID, ok := gw.activeSession[cs]
	if !ok {
		return nil, false
	}
	relay, ok := gw.relays[sessionID]
	return relay, ok
}

// GetActiveSessionID returns the active session ID for a channel endpoint.
func (gw *Gateway) GetActiveSessionID(cs ChannelSource) (string, bool) {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	id, ok := gw.activeSession[cs]
	return id, ok
}

// TouchActivity records the current time as the last activity for a source.
func (gw *Gateway) TouchActivity(cs ChannelSource) {
	gw.lastActivity.Store(cs, time.Now())
}

// GetAllStatusInfo returns StatusInfo for all active sessions.
func (gw *Gateway) GetAllStatusInfo() []acp2.StatusInfo {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	var result []acp2.StatusInfo
	for _, relay := range gw.relays {
		result = append(result, relay.Session().GetStatusInfo())
	}
	return result
}

// SendToSource sends a text message to a specific channel endpoint.
func (gw *Gateway) SendToSource(cs ChannelSource, msg string) error {
	ch, ok := gw.channels.Get(cs.ChannelID)
	if !ok {
		return fmt.Errorf("channel %s not found", cs.ChannelID)
	}
	return ch.SendTextMessage(cs.SourceID, msg)
}

// makeBroadcast creates a BroadcastFunc for a given session that fans out
// to all subscribed channel endpoints.
func (gw *Gateway) makeBroadcast(sessionID string) BroadcastFunc {
	return func(fn func(Channel, SourceID)) {
		gw.mu.RLock()
		sources := gw.sessionToSources[sessionID]
		// Copy to avoid holding lock during callbacks
		targets := make([]ChannelSource, len(sources))
		copy(targets, sources)
		gw.mu.RUnlock()

		for _, cs := range targets {
			ch, ok := gw.channels.Get(cs.ChannelID)
			if !ok {
				continue
			}
			fn(ch, cs.SourceID)
		}
	}
}

// closeIdleSessions finds and closes sessions that have been idle for longer than IdleTimeout.
func (gw *Gateway) closeIdleSessions() {
	now := time.Now()
	gw.mu.RLock()
	var toClose []struct {
		sessionID string
		cs        ChannelSource
	}
	for cs, sessionID := range gw.activeSession {
		lastTime, ok := gw.lastActivity.Load(cs)
		if !ok {
			relay, relayOk := gw.relays[sessionID]
			if relayOk {
				lastTime = relay.Session().GetStatusInfo().CreatedAt
			} else {
				continue
			}
		}
		if now.Sub(lastTime.(time.Time)) > IdleTimeout {
			toClose = append(toClose, struct {
				sessionID string
				cs        ChannelSource
			}{sessionID, cs})
		}
	}
	gw.mu.RUnlock()

	for _, item := range toClose {
		gw.mu.RLock()
		relay, ok := gw.relays[item.sessionID]
		gw.mu.RUnlock()
		if ok {
			msg := appendUsageSummary(relay, "Session closed due to inactivity.")
			_ = gw.SendToSource(item.cs, msg)
		}
		gw.CloseSession(item.sessionID)
	}
}

// --- Command handling ---

var validCommands = []string{"/start", "/stop", "/status", "/cancel", "/clear", "/exit", "/models", "/modes", "/mode", "/pwd", "/cd", "/ls", "/get", "/set"}

func (gw *Gateway) OnCommandReceived(cs ChannelSource, s string) error {
	switch {
	case s == "/start":
		if err := gw.handleStart(cs); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to start session: %v", err))
		}
	case s == "/stop":
		if err := gw.handleStop(cs); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to stop session: %v", err))
		}
	case s == "/status":
		if err := gw.handleStatus(cs); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to check status: %v", err))
		}
	case s == "/cancel":
		if err := gw.handleCancel(cs); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to cancel session: %v", err))
		}
	case s == "/clear":
		if err := gw.handleClear(cs); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to clear session: %v", err))
		}
	case s == "/exit":
		if relay, exists := gw.GetRelay(cs); exists {
			if msg := appendUsageSummary(relay, ""); msg != "" {
				_ = gw.SendToSource(cs, msg)
			}
		}
		_ = gw.SendToSource(cs, "Shutting down...")
		os.Exit(0)
	case s == "/modes":
		if err := gw.handleModes(cs); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to list modes: %v", err))
		}
	case strings.HasPrefix(s, "/mode "):
		modeId := strings.TrimPrefix(s, "/mode ")
		if err := gw.handleSetMode(cs, modeId); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to set mode: %v", err))
		}
	case s == "/pwd":
		if err := gw.handlePwd(cs); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to get working directory: %v", err))
		}
	case strings.HasPrefix(s, "/cd"):
		dir := strings.TrimSpace(strings.TrimPrefix(s, "/cd"))
		if err := gw.handleCd(cs, dir); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to change directory: %v", err))
		}
	case s == "/ls" || strings.HasPrefix(s, "/ls "):
		arg := strings.TrimSpace(strings.TrimPrefix(s, "/ls"))
		if err := gw.handleLs(cs, arg); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to list directory: %v", err))
		}
	case s == "/get" || strings.HasPrefix(s, "/get "):
		field := strings.TrimSpace(strings.TrimPrefix(s, "/get"))
		if err := gw.handleGet(cs, field); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to get project setting: %v", err))
		}
	case strings.HasPrefix(s, "/set "):
		args := strings.TrimSpace(strings.TrimPrefix(s, "/set"))
		if err := gw.handleSet(cs, args); err != nil {
			_ = gw.SendToSource(cs, fmt.Sprintf("Failed to set project setting: %v", err))
		}
	default:
		return gw.OnMessageReceived(cs, s, nil)
	}
	return nil
}

func (gw *Gateway) handleStart(cs ChannelSource) error {
	if _, exists := gw.GetRelay(cs); exists {
		return gw.SendToSource(cs, "A session is already active in this channel. Use /stop first.")
	}

	params, err := gw.resolveSessionParams(cs)
	if err != nil {
		return errors.WithStack(err)
	}

	sessionID, err := gw.StartSession(cs, params.Dir, params.Agent, params.Sandbox, params.Permission, params.ProjectName, params.Env, params.Hooks)
	if err != nil {
		return errors.WithStack(err)
	}

	gw.mu.RLock()
	relay := gw.relays[sessionID]
	gw.mu.RUnlock()

	<-relay.Ready()

	sess := relay.Session()
	if sess.GetStatus() == acp2.StatusError {
		gw.CloseSession(sessionID)
		return gw.SendToSource(cs, fmt.Sprintf("Session failed to initialize: %v", sess.GetError()))
	}

	content := fmt.Sprintf("Session started with agent: %s\nWorking directory: %s\nSession ID: %s", params.Agent, params.Dir, cs.SourceID)
	if len(params.Env) > 0 {
		content += "\nEnvironment variables:"
		for _, e := range params.Env {
			content += fmt.Sprintf("\n  %s", e)
		}
	}
	return gw.SendToSource(cs, content)
}

func (gw *Gateway) handleStop(cs ChannelSource) error {
	relay, exists := gw.GetRelay(cs)
	if !exists {
		return gw.SendToSource(cs, "No active session in this channel.")
	}

	msg := appendUsageSummary(relay, "Session stopped.")
	sessionID, _ := gw.GetActiveSessionID(cs)
	gw.CloseSession(sessionID)
	return gw.SendToSource(cs, msg)
}

func (gw *Gateway) handleStatus(cs ChannelSource) error {
	return gw.WithRelay(cs, func(relay *Relay) error {
		info := relay.Session().GetStatusInfo()
		content := fmt.Sprintf("Session active\nAgent: %s\nStatus: %s\nStarted: %s",
			info.Agent,
			info.Status,
			info.CreatedAt.Format(time.RFC3339),
		)
		if info.Sandbox != "" {
			content += fmt.Sprintf("\nSandbox: %s", info.Sandbox)
		}
		if info.SDKVersion != "" {
			content += fmt.Sprintf("\nSDK Version: %s", info.SDKVersion)
		}
		if info.Model != "" {
			content += fmt.Sprintf("\nModel: %s", info.Model)
		}
		return gw.SendToSource(cs, appendUsageSummary(relay, content))
	})
}

func (gw *Gateway) handleCancel(cs ChannelSource) error {
	return gw.WithRelay(cs, func(relay *Relay) error {
		if err := relay.Session().Cancel(); err != nil {
			return gw.SendToSource(cs, "Failed to cancel.")
		}
		return gw.SendToSource(cs, appendUsageSummary(relay, "Context is canceled."))
	})
}

func (gw *Gateway) handleClear(cs ChannelSource) error {
	return gw.WithRelay(cs, func(relay *Relay) error {
		agent := relay.Session().GetAgent()
		dir := relay.Session().GetCwd()
		sandbox := relay.Session().GetSandbox()
		env := relay.Session().GetEnv()

		// Resolve permission from topic (not stored on session)
		params, err := gw.resolveSessionParams(cs)
		if err != nil {
			return err
		}

		msg := appendUsageSummary(relay, "")

		sessionID, _ := gw.GetActiveSessionID(cs)
		gw.CloseSession(sessionID)

		newSessionID, err := gw.StartSession(cs, dir, agent, sandbox, params.Permission, params.ProjectName, env, params.Hooks)
		if err != nil {
			return errors.Wrap(err, "failed to start new session")
		}

		msg = fmt.Sprintf("Session cleared. New session started with ID: %s\nWorking directory: %s", newSessionID, dir) + msg
		return gw.SendToSource(cs, msg)
	})
}

func (gw *Gateway) handleModes(cs ChannelSource) error {
	return gw.WithRelay(cs, func(relay *Relay) error {
		modes := relay.Session().GetModes()
		if modes == nil {
			return gw.SendToSource(cs, "Mode information not available for this agent.")
		}

		var lines []string
		lines = append(lines, "Available modes:")
		for _, mode := range modes.AvailableModes {
			if mode.Id == modes.CurrentModeId {
				lines = append(lines, fmt.Sprintf("  * %s (%s) [selected]", mode.Name, mode.Id))
			} else {
				lines = append(lines, fmt.Sprintf("    %s (%s)", mode.Name, mode.Id))
			}
		}
		return gw.SendToSource(cs, strings.Join(lines, "\n"))
	})
}

func (gw *Gateway) handleSetMode(cs ChannelSource, modeId string) error {
	return gw.WithRelay(cs, func(relay *Relay) error {
		modes := relay.Session().GetModes()
		if modes == nil {
			return gw.SendToSource(cs, "Mode information not available for this agent.")
		}

		var found bool
		var modeName string
		for _, mode := range modes.AvailableModes {
			if string(mode.Id) == modeId {
				found = true
				modeName = mode.Name
				break
			}
		}
		if !found {
			return gw.SendToSource(cs, fmt.Sprintf("Unknown mode: %s", modeId))
		}

		if err := relay.Session().SetMode(modeId); err != nil {
			return gw.SendToSource(cs, fmt.Sprintf("Failed to set mode: %v", err))
		}
		return gw.SendToSource(cs, fmt.Sprintf("Mode set to: %s (%s)", modeName, modeId))
	})
}

func (gw *Gateway) handlePwd(cs ChannelSource) error {
	if relay, exists := gw.GetRelay(cs); exists {
		return gw.SendToSource(cs, relay.Session().GetCwd())
	}
	params, err := gw.resolveSessionParams(cs)
	if err != nil {
		return err
	}
	return gw.SendToSource(cs, params.Dir)
}

func (gw *Gateway) handleCd(cs ChannelSource, dir string) error {
	ch, ok := gw.channels.Get(cs.ChannelID)
	if !ok {
		return fmt.Errorf("channel %s not found", cs.ChannelID)
	}

	if dir == "" {
		if override, ok := gw.dirOverrides.Load(cs); ok {
			return gw.SendToSource(cs, fmt.Sprintf("Override dir: %s", override.(string)))
		}
		return gw.SendToSource(cs, "No directory override set. Use /cd <path> to set one.")
	}

	if !filepath.IsAbs(dir) {
		base := ""
		if relay, exists := gw.GetRelay(cs); exists {
			base = relay.Session().GetCwd()
		} else if override, ok := gw.dirOverrides.Load(cs); ok {
			base = override.(string)
		} else {
			params, err := gw.resolveSessionParams(cs)
			if err != nil {
				return err
			}
			base = params.Dir
		}
		dir = filepath.Join(base, dir)
	}

	dir = filepath.Clean(dir)

	info, err := os.Stat(dir)
	if err != nil {
		return gw.SendToSource(cs, fmt.Sprintf("Directory not found: %s", dir))
	}
	if !info.IsDir() {
		return gw.SendToSource(cs, fmt.Sprintf("Not a directory: %s", dir))
	}

	gw.dirOverrides.Store(cs, dir)

	if relay, exists := gw.GetRelay(cs); exists {
		agent := relay.Session().GetAgent()
		sandbox := relay.Session().GetSandbox()
		env := relay.Session().GetEnv()

		// Resolve permission from topic (not stored on session)
		params, err := gw.resolveSessionParams(cs)
		if err != nil {
			return err
		}

		msg := appendUsageSummary(relay, "")
		sessionID, _ := gw.GetActiveSessionID(cs)
		gw.CloseSession(sessionID)

		newSessionID, err := gw.StartSession(cs, dir, agent, sandbox, params.Permission, params.ProjectName, env, params.Hooks)
		if err != nil {
			return gw.SendToSource(cs, fmt.Sprintf("Changed to %s but failed to restart session: %v", dir, err))
		}

		gw.mu.RLock()
		newRelay := gw.relays[newSessionID]
		gw.mu.RUnlock()
		<-newRelay.Ready()

		_ = ch // already fetched above
		return gw.SendToSource(cs, fmt.Sprintf("Changed to %s. Session restarted (ID: %s)%s", dir, newSessionID, msg))
	}

	return gw.SendToSource(cs, fmt.Sprintf("Working directory set to: %s", dir))
}

func (gw *Gateway) handleLs(cs ChannelSource, arg string) error {
	dir := arg
	if dir == "" {
		if relay, exists := gw.GetRelay(cs); exists {
			dir = relay.Session().GetCwd()
		} else {
			params, err := gw.resolveSessionParams(cs)
			if err != nil {
				return err
			}
			dir = params.Dir
		}
	} else if !filepath.IsAbs(dir) {
		base := ""
		if relay, exists := gw.GetRelay(cs); exists {
			base = relay.Session().GetCwd()
		} else {
			params, err := gw.resolveSessionParams(cs)
			if err != nil {
				return err
			}
			base = params.Dir
		}
		dir = filepath.Join(base, dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return gw.SendToSource(cs, fmt.Sprintf("Cannot read directory: %v", err))
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("%s:", dir))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		lines = append(lines, "  "+name)
	}
	if len(entries) == 0 {
		lines = append(lines, "  (empty)")
	}
	return gw.SendToSource(cs, strings.Join(lines, "\n"))
}

func (gw *Gateway) handleGet(cs ChannelSource, field string) error {
	projectName := gw.resolveProjectName(cs)
	proj, err := gw.projects.GetProject(context.Background(), projectName)
	if err != nil {
		return err
	}

	if field == "" {
		// Show all settings
		var lines []string
		lines = append(lines, fmt.Sprintf("Project: %s", proj.Name))
		lines = append(lines, fmt.Sprintf("  agent: %s", proj.Agent))
		lines = append(lines, fmt.Sprintf("  dir: %s", proj.Dir))
		lines = append(lines, fmt.Sprintf("  sandbox: %s", proj.Sandbox))
		lines = append(lines, fmt.Sprintf("  permission: %s", proj.Permission))
		lines = append(lines, fmt.Sprintf("  repo: %s", proj.Repo))
		lines = append(lines, fmt.Sprintf("  hooks: %s", proj.Hooks))
		if len(proj.Env) > 0 {
			lines = append(lines, "  env:")
			for _, e := range proj.Env {
				lines = append(lines, fmt.Sprintf("    %s", e))
			}
		} else {
			lines = append(lines, "  env: (none)")
		}
		return gw.SendToSource(cs, strings.Join(lines, "\n"))
	}

	switch field {
	case "agent":
		return gw.SendToSource(cs, fmt.Sprintf("agent: %s", proj.Agent))
	case "dir":
		return gw.SendToSource(cs, fmt.Sprintf("dir: %s", proj.Dir))
	case "sandbox":
		return gw.SendToSource(cs, fmt.Sprintf("sandbox: %s", proj.Sandbox))
	case "permission":
		return gw.SendToSource(cs, fmt.Sprintf("permission: %s", proj.Permission))
	case "repo":
		return gw.SendToSource(cs, fmt.Sprintf("repo: %s", proj.Repo))
	case "env":
		if len(proj.Env) == 0 {
			return gw.SendToSource(cs, "env: (none)")
		}
		return gw.SendToSource(cs, fmt.Sprintf("env: %s", strings.Join(proj.Env, ", ")))
	case "hooks":
		return gw.SendToSource(cs, fmt.Sprintf("hooks: %s", proj.Hooks))
	default:
		return gw.SendToSource(cs, fmt.Sprintf("Unknown field: %s. Valid fields: agent, dir, sandbox, permission, repo, env, hooks", field))
	}
}

func (gw *Gateway) handleSet(cs ChannelSource, args string) error {
	parts := strings.SplitN(args, " ", 2)
	field := parts[0]
	value := ""
	if len(parts) > 1 {
		value = strings.TrimSpace(parts[1])
	}

	if field == "" {
		return gw.SendToSource(cs, "Usage: /set <field> <value>\nFields: agent, dir, sandbox, permission, repo, env, hooks")
	}

	projectName := gw.resolveProjectName(cs)

	if field == "env" {
		if value == "" {
			if err := gw.projects.ClearProjectEnv(context.Background(), projectName); err != nil {
				return err
			}
			return gw.SendToSource(cs, "Cleared all env vars.")
		}
		if err := gw.projects.AppendProjectEnv(context.Background(), projectName, value); err != nil {
			return err
		}
		return gw.SendToSource(cs, fmt.Sprintf("Added env: %s", value))
	}

	if err := gw.projects.SetProjectField(context.Background(), projectName, field, value); err != nil {
		return gw.SendToSource(cs, fmt.Sprintf("Failed to set %s: %v", field, err))
	}
	return gw.SendToSource(cs, fmt.Sprintf("Set %s = %s", field, value))
}

// parseShellCommand checks if a message is a shell command (! or !! prefix).
// Returns the command string, whether to use sandbox, and whether it is a shell command.
func parseShellCommand(message string) (command string, useSandbox bool, isShell bool) {
	trimmed := strings.TrimSpace(message)
	if strings.HasPrefix(trimmed, "!!") {
		return strings.TrimPrefix(trimmed, "!!"), false, true
	}
	if strings.HasPrefix(trimmed, "!") {
		return strings.TrimPrefix(trimmed, "!"), true, true
	}
	return "", false, false
}

// executeShellCommand runs a shell command in the given directory, optionally
// through a sandbox, and returns the formatted output string.
func executeShellCommand(ctx context.Context, command, dir, sandbox string, useSandbox bool) string {
	var cmd *exec.Cmd
	if useSandbox && sandbox != "" {
		args := strings.Fields(sandbox)
		args = append(args, "sh", "-c", command)
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = dir
	slog.Info("shell command", "command", command, "dir", dir, "sandbox", sandbox, "useSandbox", useSandbox)
	output, err := cmd.CombinedOutput()

	result := strings.TrimRight(string(output), "\n")
	if err != nil {
		if result != "" {
			result += "\n"
		}
		result += "error: " + err.Error()
	}
	if result == "" {
		result = "(no output)"
	}

	return "$ " + command + "\n```\n" + result + "\n```"
}

func (gw *Gateway) handleShellCommand(cs ChannelSource, command string, useSandbox bool) error {
	dir := ""
	sandbox := ""
	if relay, exists := gw.GetRelay(cs); exists {
		dir = relay.Session().GetCwd()
		sandbox = relay.Session().GetSandbox()
	} else {
		params, err := gw.resolveSessionParams(cs)
		if err != nil {
			return gw.SendToSource(cs, fmt.Sprintf("Failed to resolve directory: %v", err))
		}
		dir = params.Dir
		sandbox = params.Sandbox
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	msg := executeShellCommand(ctx, command, dir, sandbox, useSandbox)

	if len(msg) > 1900 {
		ch, ok := gw.channels.Get(cs.ChannelID)
		if !ok {
			return fmt.Errorf("channel %s not found", cs.ChannelID)
		}
		// Extract raw result from formatted message for file attachment.
		start := strings.Index(msg, "```\n") + 4
		end := strings.LastIndex(msg, "\n```")
		return ch.SendFile(cs.SourceID, "output.txt", []byte(msg[start:end]))
	}
	return gw.SendToSource(cs, msg)
}

// handleShellCommandBySessionID executes a shell command for a web session
// and publishes the result as events (prompt + agent_message_chunk + prompt_finished).
func (gw *Gateway) handleShellCommandBySessionID(sessionID string, relay *Relay, command string, useSandbox bool, originalMessage string) error {
	sess := relay.Session()
	dir := sess.GetCwd()
	sandbox := sess.GetSandbox()

	// Log the prompt event (shows the original "!command" in the conversation).
	promptPayload, _ := json.Marshal(map[string]string{"prompt": originalMessage})
	if dbErr := gw.store.InsertLog(context.Background(), sessionID, "prompt", promptPayload); dbErr != nil {
		slog.Error("failed to insert prompt log", "session", sessionID, "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sessionID, "prompt", promptPayload)
	}

	// Execute the shell command.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result := executeShellCommand(ctx, command, dir, sandbox, useSandbox)

	// Publish the result as an agent_message_chunk event.
	chunkPayload, _ := json.Marshal(map[string]any{
		"content": map[string]string{
			"type": "text",
			"text": result,
		},
	})
	if dbErr := gw.store.InsertLog(context.Background(), sessionID, "agent_message_chunk", chunkPayload); dbErr != nil {
		slog.Error("failed to insert shell result log", "session", sessionID, "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sessionID, "agent_message_chunk", chunkPayload)
	}

	// Publish prompt_finished event.
	finishedPayload, _ := json.Marshal(map[string]any{"duration_ms": int64(0)})
	if dbErr := gw.store.InsertLog(context.Background(), sessionID, "prompt_finished", finishedPayload); dbErr != nil {
		slog.Error("failed to insert prompt_finished log", "session", sessionID, "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sessionID, "prompt_finished", finishedPayload)
	}

	return nil
}

// --- Message handling ---

func (gw *Gateway) OnMessageReceived(cs ChannelSource, message string, images []ImageData) error {
	if len(strings.Split(message, "\n")) == 1 && len(message) > 1 && message[0] == 226 && message[len(message)-1] == 169 {
		cmd := message[4 : len(message)-4]
		for _, v := range validCommands {
			if cmd == v {
				return gw.OnCommandReceived(cs, cmd)
			}
		}
		message = cmd
	}

	// Accept bare command names without '/' prefix
	trimmed := strings.TrimSpace(message)
	if !strings.Contains(trimmed, "\n") {
		word := trimmed
		if idx := strings.IndexByte(trimmed, ' '); idx != -1 {
			word = trimmed[:idx]
		}
		candidate := "/" + word
		for _, v := range validCommands {
			if candidate == v {
				return gw.OnCommandReceived(cs, "/"+trimmed)
			}
		}
	}

	// Handle shell commands: !! runs without sandbox, ! runs with sandbox
	if cmd, useSandbox, isShell := parseShellCommand(trimmed); isShell {
		return gw.handleShellCommand(cs, cmd, useSandbox)
	}

	ch, ok := gw.channels.Get(cs.ChannelID)
	if !ok {
		return fmt.Errorf("channel %s not found", cs.ChannelID)
	}

	relay, exists := gw.GetRelay(cs)
	if !exists {
		var err error
		relay, err = gw.autoCreateSession(cs)
		if err != nil {
			return errors.WithStack(err)
		}
		sess := relay.Session()
		<-relay.Ready()
		if sess.GetStatus() == acp2.StatusError {
			_ = gw.SendToSource(cs, fmt.Sprintf("Session failed to initialize: %v", sess.GetError()))
			sessionID, _ := gw.GetActiveSessionID(cs)
			gw.CloseSession(sessionID)
			return nil
		}
	}

	sess := relay.Session()

	<-relay.Ready()
	if sess.GetStatus() == acp2.StatusError {
		_ = gw.SendToSource(cs, fmt.Sprintf("Session failed to initialize: %v", sess.GetError()))
		sessionID, _ := gw.GetActiveSessionID(cs)
		gw.CloseSession(sessionID)
		return nil
	}

	message = strings.Trim(message, "`")

	// Run BeforeFirstPrompt hooks on the first user prompt
	if sess.GetStatusInfo().Usage.PromptCount == 0 {
		for _, hook := range gw.getHooks(sess.GetID()) {
			message = hook.BeforeFirstPrompt(sess.GetCwd(), message)
		}
	}
	gw.TouchActivity(cs)

	err := ch.StartConversation(cs.SourceID)
	if err != nil {
		_ = gw.SendToSource(cs, fmt.Sprintf("Error sending prompt: %v", err))
	}

	// Build content blocks from text + images
	var blocks []acp.ContentBlock
	if message != "" {
		blocks = append(blocks, acp.TextBlock(message))
	}
	for _, img := range images {
		encoded := base64.StdEncoding.EncodeToString(img.Data)
		blocks = append(blocks, acp.ImageBlock(encoded, img.MimeType))
	}

	// Log the prompt event (text only, skip image data)
	logText := message
	if logText == "" && len(images) > 0 {
		logText = fmt.Sprintf("[%d image(s)]", len(images))
	}
	promptPayload, _ := json.Marshal(map[string]string{"prompt": logText})
	if dbErr := gw.store.InsertLog(context.Background(), sess.GetID(), "prompt", promptPayload); dbErr != nil {
		slog.Error("failed to insert prompt log", "session", sess.GetID(), "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sess.GetID(), "prompt", promptPayload)
	}

	promptStart := time.Now()
	resp, err := sess.Prompt(blocks)
	promptDuration := time.Since(promptStart)
	if err != nil {
		_ = gw.SendToSource(cs, fmt.Sprintf("Error sending prompt: %v", err))
	}

	// Wait for the Relay to finish processing all streaming events for this
	// prompt. Prompt() returns as soon as the JSON-RPC response arrives, but
	// session_update notifications (text fragments) may still be queued in
	// the updates channel. Without this, FinishConversation would flush the
	// buffer before the last fragments have been written.
	relay.WaitForPending()

	// Log prompt-finished event
	finishedPayload, _ := json.Marshal(map[string]any{"duration_ms": promptDuration.Milliseconds()})
	if dbErr := gw.store.InsertLog(context.Background(), sess.GetID(), "prompt_finished", finishedPayload); dbErr != nil {
		slog.Error("failed to insert prompt_finished log", "session", sess.GetID(), "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sess.GetID(), "prompt_finished", finishedPayload)
	}

	// Accumulate prompt duration
	if dbErr := gw.store.AddPromptDuration(context.Background(), sess.GetID(), promptDuration.Milliseconds()); dbErr != nil {
		slog.Error("failed to add prompt duration", "session", sess.GetID(), "err", dbErr)
	}

	// Persist updated usage/status
	if dbErr := gw.store.UpdateSession(context.Background(), sess.GetID(), sess.GetStatusInfo()); dbErr != nil {
		slog.Error("failed to update session in db", "session", sess.GetID(), "err", dbErr)
	}


	// Run AfterPromptFinished hooks
	for _, hook := range gw.getHooks(sess.GetID()) {
		hook.AfterPromptFinished(sess.GetCwd(), message, func(followUp string) error {
			return gw.sendPromptInternal(sess.GetID(), relay, followUp)
		})
	}
	err = ch.FinishConversation(cs.SourceID, resp)
	if err != nil {
		_ = gw.SendToSource(cs, fmt.Sprintf("Error sending prompt: %v", err))
	}

	return nil
}

func (gw *Gateway) autoCreateSession(cs ChannelSource) (*Relay, error) {
	params, err := gw.resolveSessionParams(cs)
	if err != nil {
		return nil, err
	}

	sessionID, err := gw.StartSession(cs, params.Dir, params.Agent, params.Sandbox, params.Permission, params.ProjectName, params.Env, params.Hooks)
	if err != nil {
		return nil, fmt.Errorf("failed to start agent: %w", err)
	}

	msg := fmt.Sprintf("Session started (agent: %s, dir: %s)", params.Agent, params.Dir)
	if len(params.Env) > 0 {
		msg += fmt.Sprintf("\nEnv: %s", strings.Join(params.Env, ", "))
	}
	_ = gw.SendToSource(cs, msg)

	gw.mu.RLock()
	relay := gw.relays[sessionID]
	gw.mu.RUnlock()
	return relay, nil
}

// WithRelay runs fn with the active relay for cs, or sends "no active session" message.
func (gw *Gateway) WithRelay(cs ChannelSource, fn func(relay *Relay) error) error {
	relay, exists := gw.GetRelay(cs)
	if !exists {
		return gw.SendToSource(cs, "No active session in this channel.")
	}
	return fn(relay)
}

// appendUsageSummary appends the session's token usage summary to msg if available.
func appendUsageSummary(relay *Relay, msg string) string {
	if summary := relay.Session().GetStatusInfo().FormatUsageSummary(); summary != "" {
		msg += "\n" + summary
	}
	return msg
}


// sendPromptInternal sends a prompt through the full pipeline (log, send, wait, persist)
// without invoking hooks. Used both for hook-generated follow-up prompts and as the
// core of SendPromptBySessionID.
func (gw *Gateway) sendPromptInternal(sessionID string, relay *Relay, message string) error {
	sess := relay.Session()

	promptPayload, _ := json.Marshal(map[string]string{"prompt": message})
	if dbErr := gw.store.InsertLog(context.Background(), sessionID, "prompt", promptPayload); dbErr != nil {
		slog.Error("failed to insert prompt log", "session", sessionID, "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sessionID, "prompt", promptPayload)
	}

	promptStart := time.Now()
	_, err := sess.Prompt([]acp.ContentBlock{acp.TextBlock(message)})
	relay.WaitForPending()
	promptDuration := time.Since(promptStart)

	finishedPayload, _ := json.Marshal(map[string]any{"duration_ms": promptDuration.Milliseconds()})
	if dbErr := gw.store.InsertLog(context.Background(), sessionID, "prompt_finished", finishedPayload); dbErr != nil {
		slog.Error("failed to insert prompt_finished log", "session", sessionID, "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sessionID, "prompt_finished", finishedPayload)
	}

	if dbErr := gw.store.AddPromptDuration(context.Background(), sessionID, promptDuration.Milliseconds()); dbErr != nil {
		slog.Error("failed to add prompt duration", "session", sessionID, "err", dbErr)
	}

	if dbErr := gw.store.UpdateSession(context.Background(), sessionID, sess.GetStatusInfo()); dbErr != nil {
		slog.Error("failed to update session in db", "session", sessionID, "err", dbErr)
	}

	return err
}

// HandleCommandBySessionID handles slash commands for web sessions (no ChannelSource).
// Returns a message to display and optionally a new session ID (for /clear).
// The caller is responsible for publishing the result to the client.
func (gw *Gateway) HandleCommandBySessionID(sessionID string, command string) (string, string, error) {
	gw.mu.RLock()
	relay, ok := gw.relays[sessionID]
	gw.mu.RUnlock()

	if !ok {
		return "", "", fmt.Errorf("session %s not found or not running", sessionID)
	}

	trimmed := strings.TrimSpace(command)

	switch {
	case trimmed == "/status":
		info := relay.Session().GetStatusInfo()
		content := fmt.Sprintf("Session active\nAgent: %s\nStatus: %s\nStarted: %s",
			info.Agent,
			info.Status,
			info.CreatedAt.Format(time.RFC3339),
		)
		if info.Sandbox != "" {
			content += fmt.Sprintf("\nSandbox: %s", info.Sandbox)
		}
		if info.SDKVersion != "" {
			content += fmt.Sprintf("\nSDK Version: %s", info.SDKVersion)
		}
		if info.Model != "" {
			content += fmt.Sprintf("\nModel: %s", info.Model)
		}
		content = appendUsageSummary(relay, content)
		return content, "", nil

	case trimmed == "/clear":
		sess := relay.Session()
		agent := sess.GetAgent()
		dir := sess.GetCwd()
		sandbox := sess.GetSandbox()

		msg := appendUsageSummary(relay, "")

		// Look up the project name so the new session belongs to the same project.
		gw.mu.RLock()
		projectName := gw.sessionProjects[sessionID]
		gw.mu.RUnlock()
		if projectName == "" {
			projectName = filepath.Base(dir)
		}

		gw.CloseSession(sessionID)

		newSessionID, err := gw.StartSessionWeb(dir, agent, sandbox, projectName)
		if err != nil {
			return "", "", errors.Wrap(err, "failed to start new session")
		}

		msg = fmt.Sprintf("Session cleared. New session started with ID: %s\nWorking directory: %s", newSessionID, dir) + msg
		return msg, newSessionID, nil

	case trimmed == "/cancel":
		if err := relay.Session().Cancel(); err != nil {
			return "Failed to cancel.", "", nil
		}
		return appendUsageSummary(relay, "Context is canceled."), "", nil

	case trimmed == "/stop":
		msg := appendUsageSummary(relay, "Session stopped.")
		gw.CloseSession(sessionID)
		return msg, "", nil

	default:
		return "", "", fmt.Errorf("unknown command: %s", trimmed)
	}
}

// SendPromptBySessionID sends a prompt directly to a session by its ID.
// This is used by the web UI where there is no ChannelSource.
func (gw *Gateway) SendPromptBySessionID(sessionID string, message string) error {
	gw.mu.RLock()
	relay, ok := gw.relays[sessionID]
	gw.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %s not found or not running", sessionID)
	}

	sess := relay.Session()

	// Wait for the session to be fully initialized (ACP protocol handshake
	// and session creation). Without this, Prompt() races with init() and
	// may operate on an uninitialized connection.
	<-relay.Ready()
	if sess.GetStatus() == acp2.StatusError {
		return fmt.Errorf("session %s failed to initialize: %s", sessionID, sess.GetError())
	}

	// Handle shell commands (! and !!)
	if cmd, useSandbox, isShell := parseShellCommand(message); isShell {
		return gw.handleShellCommandBySessionID(sessionID, relay, cmd, useSandbox, message)
	}

	// Run BeforeFirstPrompt hooks on the first user prompt
	if sess.GetStatusInfo().Usage.PromptCount == 0 {
		for _, hook := range gw.getHooks(sessionID) {
			message = hook.BeforeFirstPrompt(sess.GetCwd(), message)
		}
	}

	// Log the prompt event
	promptPayload, _ := json.Marshal(map[string]string{"prompt": message})
	if dbErr := gw.store.InsertLog(context.Background(), sessionID, "prompt", promptPayload); dbErr != nil {
		slog.Error("failed to insert prompt log", "session", sessionID, "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sessionID, "prompt", promptPayload)
	}

	promptStart := time.Now()
	_, err := sess.Prompt([]acp.ContentBlock{acp.TextBlock(message)})
	relay.WaitForPending()
	promptDuration := time.Since(promptStart)

	// Log prompt-finished event
	finishedPayload, _ := json.Marshal(map[string]any{"duration_ms": promptDuration.Milliseconds()})
	if dbErr := gw.store.InsertLog(context.Background(), sessionID, "prompt_finished", finishedPayload); dbErr != nil {
		slog.Error("failed to insert prompt_finished log", "session", sessionID, "err", dbErr)
	}
	if gw.publisher != nil {
		gw.publisher.PublishEvent(sessionID, "prompt_finished", finishedPayload)
	}

	// Accumulate prompt duration
	if dbErr := gw.store.AddPromptDuration(context.Background(), sessionID, promptDuration.Milliseconds()); dbErr != nil {
		slog.Error("failed to add prompt duration", "session", sessionID, "err", dbErr)
	}

	// Persist updated usage/status
	if dbErr := gw.store.UpdateSession(context.Background(), sessionID, sess.GetStatusInfo()); dbErr != nil {
		slog.Error("failed to update session in db", "session", sessionID, "err", dbErr)
	}


	// Run AfterPromptFinished hooks
	for _, hook := range gw.getHooks(sessionID) {
		hook.AfterPromptFinished(sess.GetCwd(), message, func(followUp string) error {
			return gw.sendPromptInternal(sessionID, relay, followUp)
		})
	}

	return err
}

// --- Parameter resolution ---

type sessionParams struct {
	Agent       string
	Dir         string
	Sandbox     string
	Permission  string
	Env         []string
	ProjectName string
	Hooks       string
}

// resolveProjectName returns the project name for a channel source.
// It uses the channel name (e.g. Discord channel name) as the project name.
func (gw *Gateway) resolveProjectName(cs ChannelSource) string {
	ch, ok := gw.channels.Get(cs.ChannelID)
	if !ok {
		return string(cs.SourceID)
	}
	name, err := ch.GetChannelName(cs.SourceID)
	if err != nil || name == "" {
		return string(cs.SourceID)
	}
	return name
}

func (gw *Gateway) resolveSessionParams(cs ChannelSource) (sessionParams, error) {
	ch, ok := gw.channels.Get(cs.ChannelID)
	if !ok {
		return sessionParams{}, fmt.Errorf("channel %s not found", cs.ChannelID)
	}

	projectName := gw.resolveProjectName(cs)
	proj, err := gw.projects.GetProject(context.Background(), projectName)
	if err != nil {
		return sessionParams{}, fmt.Errorf("failed to get project %s: %w", projectName, err)
	}

	agent := proj.Agent
	dir := proj.Dir
	sandbox := proj.Sandbox
	permission := proj.Permission
	env := append([]string{}, proj.Env...)

	if agent == "" {
		agent = gw.config.Defaults.Agent
	}
	agent = gw.config.ResolveAgent(agent)
	if sandbox == "none" {
		sandbox = ""
	} else if sandbox == "" {
		sandbox = gw.config.Defaults.Sandbox
	}
	if sandbox == "none" {
		sandbox = ""
	}

	// Resolve relative sandbox command against sandbox_dir
	if sandbox != "" && gw.config.SandboxDir != "" {
		parts := strings.Fields(sandbox)
		if !filepath.IsAbs(parts[0]) {
			parts[0] = filepath.Join(gw.config.SandboxDir, parts[0])
			sandbox = strings.Join(parts, " ")
		}
	}

	// Prepend whitelisted env vars from process environment
	if len(gw.config.Defaults.EnvWhitelist) > 0 {
		projEnv := env
		env = nil
		projKeys := make(map[string]bool)
		for _, e := range projEnv {
			if k, _, ok := strings.Cut(e, "="); ok {
				projKeys[k] = true
			}
		}
		for _, key := range gw.config.Defaults.EnvWhitelist {
			if projKeys[key] {
				continue
			}
			if val, ok := os.LookupEnv(key); ok {
				env = append(env, key+"="+val)
			}
		}
		env = append(env, projEnv...)
	}

	// Per-channel dir override from /cd takes priority
	if override, ok := gw.dirOverrides.Load(cs); ok {
		dir = override.(string)
	}
	if dir == "" {
		dir = gw.resolveDirectoryFromSearchPath(cs, ch)
		if dir != "" {
			// Persist the resolved directory back to the project so future
			// sessions use it directly without re-scanning search_path.
			_ = gw.projects.SetProjectField(context.Background(), projectName, "dir", dir)
		}
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return sessionParams{}, fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	return sessionParams{
		Agent:       agent,
		Dir:         dir,
		Sandbox:     sandbox,
		Permission:  permission,
		Env:         env,
		ProjectName: projectName,
		Hooks:       proj.Hooks,
	}, nil
}

func (gw *Gateway) resolveDirectoryFromSearchPath(cs ChannelSource, ch Channel) string {
	if len(gw.config.SearchPath) == 0 {
		return ""
	}

	channelName, err := ch.GetChannelName(cs.SourceID)
	if err != nil || channelName == "" {
		return ""
	}

	for _, searchDir := range gw.config.SearchPath {
		candidate := filepath.Join(searchDir, channelName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

// extractBacktickCommand extracts a command from backticks if the message
// consists of a single backtick-wrapped command.
func extractBacktickCommand(content string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "`/") && strings.HasSuffix(trimmed, "`") {
		inner := trimmed[1 : len(trimmed)-1]
		if !strings.Contains(inner, "`") {
			return inner
		}
	}
	return content
}

// marshalEvent attempts to marshal a SessionUpdate to JSON. If marshaling fails
// (e.g. due to truncated ContentBlock data from the agent), it returns a
// metadata-only fallback JSON with the event type and error message.
func marshalEvent(update acp.SessionUpdate) (json.RawMessage, string) {
	eventType := db.ClassifyEvent(update)
	raw, err := json.Marshal(update)
	if err != nil {
		slog.Debug("marshal event fallback", "type", eventType, "err", err)
		raw = []byte(`{"_marshalError":` + strconv.Quote(err.Error()) + `,"_eventType":"` + eventType + `"}`)
	}
	return raw, eventType
}

// detectGitCommit returns the short git commit hash for the given directory.
// If the working tree is dirty, it appends "-dirty". Returns "" if dir is not a git repo.
func detectGitCommit(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return ""
	}

	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err = cmd.Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		commit += "-dirty"
	}
	return commit
}

// --- Helpers ---

func removeString(slice []ChannelSource, item ChannelSource) []ChannelSource {
	result := slice[:0]
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

func removeStringFromSlice(slice []string, item string) []string {
	result := slice[:0]
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}
