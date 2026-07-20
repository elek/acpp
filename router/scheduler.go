package router

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/sandbox"
	"github.com/elek/acpp/types"
	"github.com/robfig/cron/v3"
)

// conversations is the slice of router behaviour the scheduler depends on. The
// concrete *Router satisfies it; tests substitute a fake so scheduling logic can
// be exercised without spawning real agent subprocesses.
type conversations interface {
	Create(ctx context.Context, opts types.SessionOpts) (types.ConversationMeta, error)
	WaitReady(ctx context.Context, id types.ConversationMeta) (types.ConversationMeta, error)
	Send(ctx context.Context, id types.ConversationMeta, msg any) error
	CloseConversation(id types.ConversationMeta)
	Active(conversationID string) bool
}

// Scheduler runs cron-triggered prompts as conversations on the router. Each job
// maps to a project (its settings are upserted on Start); when its schedule
// fires, a conversation is created and the prompt file is sent. The scheduler is
// itself a router.Subscriber: it observes each conversation's PromptResponse to
// learn when the turn finishes, then (unless the job reuses its session) closes
// the conversation and frees the job to run again. Nothing blocks waiting for a
// turn — completion is handled as an event, like every other router consumer.
type Scheduler struct {
	cron     *cron.Cron
	config   *config.Config
	projects db.ProjectStore
	conv     conversations

	// ctx is the lifetime context set by Start; job runs use it so a shutdown
	// cancels session creation. nil when a job is invoked directly (e.g. tests).
	ctx context.Context

	mu sync.Mutex
	// running guards against overlapping runs of the same job; an entry is held
	// from the moment a job starts until its turn's PromptResponse is observed.
	running map[string]bool
	// reusable holds the conversation kept alive for each reuse_session job.
	reusable map[string]types.ConversationMeta
	// inflight maps a running conversation's id to the job that started it, so the
	// PromptResponse handler can finish the right job.
	inflight map[string]config.ScheduledJob
}

// NewScheduler creates a Scheduler. conv is the router (or a test fake); pass nil
// to disable session creation entirely (jobs then no-op). The caller must
// subscribe Receive to the router so turn completions reach the scheduler.
func NewScheduler(conv conversations, cfg *config.Config, projects db.ProjectStore) *Scheduler {
	return &Scheduler{
		cron:     cron.New(),
		conv:     conv,
		config:   cfg,
		projects: projects,
		running:  make(map[string]bool),
		reusable: make(map[string]types.ConversationMeta),
		inflight: make(map[string]config.ScheduledJob),
	}
}

// Receive is the router.Subscriber hook that ends a scheduled turn. A
// PromptResponse marks the normal end of the turn; a ConversationClosed marks an
// abnormal one — the agent subprocess exited before the turn completed, so no
// PromptResponse will ever arrive. Either way the scheduler closes the
// conversation (unless the job reuses it) and releases the job to run on its
// next tick; without handling the closed case a wedged turn would skip every
// later tick as "previous run still active".
func (s *Scheduler) Receive(_ context.Context, _ *json.RawMessage, id types.ConversationMeta, msg any) {
	var reason string
	switch msg.(type) {
	case acp.PromptResponse:
		reason = "turn completed"
	case types.ConversationClosed:
		reason = "conversation closed before turn completed"
	default:
		return
	}
	s.mu.Lock()
	job, ok := s.inflight[id.ConversationID]
	s.mu.Unlock()
	if !ok {
		return
	}
	slog.Info("scheduled job finished", "job", job.Name, "conversation", id.ConversationID, "reason", reason)
	s.finish(id, job)
}

// Start upserts project settings and registers all scheduled jobs.
func (s *Scheduler) Start(ctx context.Context) error {
	s.ctx = ctx
	for _, job := range s.config.ScheduledJobs {
		if err := s.upsertProject(ctx, job); err != nil {
			slog.Error("failed to upsert scheduled job project", "job", job.Name, "err", err)
			continue
		}

		j := job // capture loop var
		_, err := s.cron.AddFunc(j.Schedule, func() {
			s.runJob(j)
		})
		if err != nil {
			slog.Error("failed to register cron schedule", "job", j.Name, "schedule", j.Schedule, "err", err)
			continue
		}
		slog.Info("registered scheduled job", "job", j.Name, "schedule", j.Schedule)
	}

	s.cron.Start()

	go func() {
		<-ctx.Done()
		s.cron.Stop()
	}()

	return nil
}

// upsertProject creates or updates the project with the job's settings.
func (s *Scheduler) upsertProject(ctx context.Context, job config.ScheduledJob) error {
	fields := map[string]string{
		"dir": job.Dir,
	}
	if job.Agent != "" {
		fields["agent"] = job.Agent
	}
	if job.Sandbox != "" {
		fields["sandbox"] = job.Sandbox
	}
	if job.SandboxProfiles != "" {
		fields["sandbox_profiles"] = job.SandboxProfiles
	}
	if job.Permission != "" {
		fields["permission"] = job.Permission
	}
	if job.Hooks != "" {
		fields["hooks"] = job.Hooks
	}

	for field, value := range fields {
		if err := s.projects.SetProjectField(ctx, job.Name, field, value); err != nil {
			return err
		}
	}

	if len(job.Env) > 0 {
		if err := s.projects.ClearProjectEnv(ctx, job.Name); err != nil {
			return err
		}
		for _, entry := range job.Env {
			if err := s.projects.AppendProjectEnv(ctx, job.Name, entry); err != nil {
				return err
			}
		}
	}

	return nil
}

// runJob fires a single scheduled job: it reads the prompt file, acquires a
// conversation (reusing the kept-alive one or creating a fresh one), and submits
// the prompt without blocking. The job stays marked running until Receive
// observes its PromptResponse. Overlapping runs of the same job are skipped.
func (s *Scheduler) runJob(job config.ScheduledJob) {
	s.mu.Lock()
	if s.running[job.Name] {
		s.mu.Unlock()
		slog.Warn("skipping scheduled job, previous run still active", "job", job.Name)
		return
	}
	s.running[job.Name] = true
	s.mu.Unlock()

	slog.Info("starting scheduled job", "job", job.Name)

	// clearRunning releases the job on any path that does not hand off to Receive.
	clearRunning := func() {
		s.mu.Lock()
		delete(s.running, job.Name)
		s.mu.Unlock()
	}

	promptBytes, err := os.ReadFile(job.Prompt)
	if err != nil {
		slog.Error("failed to read prompt file", "job", job.Name, "path", job.Prompt, "err", err)
		clearRunning()
		return
	}
	prompt := string(promptBytes)

	if s.conv == nil {
		slog.Error("no router available for scheduled job", "job", job.Name)
		clearRunning()
		return
	}

	agent := job.Agent
	if agent == "" {
		agent = s.config.Defaults.Agent
	}
	agent = s.config.ResolveAgent(agent)

	sandboxType := job.Sandbox
	if sandboxType == "" {
		sandboxType = s.config.Defaults.Sandbox
	}

	meta, ok := s.acquireConversation(job, agent, sandboxType)
	if !ok {
		clearRunning()
		return
	}

	// Hand the job off to Receive, which finishes it when the turn completes.
	s.mu.Lock()
	s.inflight[meta.ConversationID] = job
	s.mu.Unlock()

	// Wait for the session to finish initializing so the SessionID is known, then
	// fire the prompt via the generic Send (it does not block for the turn).
	ready, err := s.conv.WaitReady(s.context(), meta)
	if err != nil {
		slog.Error("scheduled job prompt failed", "job", job.Name, "err", err)
		// No PromptResponse will arrive, so finish here instead of in Receive.
		s.finish(meta, job)
		return
	}
	if err := s.conv.Send(s.context(), ready, acp.PromptRequest{
		SessionId: ready.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
	}); err != nil {
		slog.Error("scheduled job prompt failed", "job", job.Name, "err", err)
		// No PromptResponse will arrive, so finish here instead of in Receive.
		s.finish(meta, job)
	}
}

// acquireConversation returns the conversation to run the job on: the job's
// kept-alive one when it reuses and is still active, otherwise a freshly created
// one (recorded as reusable for reuse jobs). ok is false if creation failed.
func (s *Scheduler) acquireConversation(job config.ScheduledJob, agent, sandboxType string) (types.ConversationMeta, bool) {
	if job.ReuseSession {
		s.mu.Lock()
		existing, ok := s.reusable[job.Name]
		s.mu.Unlock()
		if ok && s.conv.Active(existing.ConversationID) {
			slog.Info("reusing conversation for scheduled job", "job", job.Name, "conversation", existing.ConversationID)
			return existing, true
		}
		if ok {
			slog.Info("reusable conversation no longer active, creating a new one", "job", job.Name, "old", existing.ConversationID)
		}
	}

	opts, err := s.opts(job, agent, sandboxType)
	if err != nil {
		slog.Error("failed to resolve sandbox for scheduled job", "job", job.Name, "err", err)
		return types.ConversationMeta{}, false
	}
	meta, err := s.conv.Create(s.context(), opts)
	if err != nil {
		slog.Error("failed to create scheduled conversation", "job", job.Name, "err", err)
		return types.ConversationMeta{}, false
	}
	slog.Info("scheduled job conversation created", "job", job.Name, "conversation", meta.ConversationID, "reuse", job.ReuseSession)

	if job.ReuseSession {
		s.mu.Lock()
		s.reusable[job.Name] = meta
		s.mu.Unlock()
	}
	return meta, true
}

// finish releases a job whose turn ended (or could not start) and closes its
// conversation unless the job reuses it. It is the shared tail of Receive and the
// submit-error path.
func (s *Scheduler) finish(meta types.ConversationMeta, job config.ScheduledJob) {
	s.mu.Lock()
	delete(s.inflight, meta.ConversationID)
	delete(s.running, job.Name)
	s.mu.Unlock()
	if !job.ReuseSession {
		s.conv.CloseConversation(meta)
	}
}

// opts builds the session options for a job from its resolved agent and sandbox.
func (s *Scheduler) opts(job config.ScheduledJob, agent, sandboxType string) (types.SessionOpts, error) {
	opts := types.SessionOpts{
		ProjectID:   job.Name,
		Agent:       agent,
		CWD:         job.Dir,
		Env:         job.Env,
		Source:      "schedule",
		SandboxType: sandboxType,
	}
	if sandboxType != "" {
		sb, err := sandbox.ResolveSandbox(sandboxType, job.SandboxProfiles, job.Dir)
		if err != nil {
			return opts, err
		}
		opts.Sandbox = sb
	}
	return opts, nil
}

// context returns the scheduler's lifetime context, or a background context when
// the scheduler was not started (e.g. in tests that call runJob directly).
func (s *Scheduler) context() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
