package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// fakeConversations records scheduler calls and tracks liveness, standing in for
// the router so scheduling logic can be tested without real agent subprocesses.
// Send is non-blocking (as the router's is); turn completion is delivered
// separately by calling Scheduler.Receive with a PromptResponse.
type fakeConversations struct {
	mu        sync.Mutex
	created   []types.ConversationMeta
	submitted []string        // prompt texts passed to Send, in order
	closed    []string        // ConversationIDs closed
	active    map[string]bool // ConversationID -> live
	next      int
}

func newFakeConversations() *fakeConversations {
	return &fakeConversations{active: make(map[string]bool)}
}

func (f *fakeConversations) Create(_ context.Context, opts types.SessionOpts) (types.ConversationMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("conv-%d", f.next)
	meta := types.ConversationMeta{
		ConversationID: id,
		ProjectID:      opts.ProjectID,
		SessionID:      acp.SessionId("sess-" + id),
	}
	f.created = append(f.created, meta)
	f.active[id] = true
	return meta, nil
}

func (f *fakeConversations) WaitReady(_ context.Context, id types.ConversationMeta) (types.ConversationMeta, error) {
	// Create already populates the SessionID, so the conversation is ready at once.
	return id, nil
}

func (f *fakeConversations) Send(_ context.Context, _ types.ConversationMeta, msg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pr, ok := msg.(acp.PromptRequest); ok && len(pr.Prompt) > 0 && pr.Prompt[0].Text != nil {
		f.submitted = append(f.submitted, pr.Prompt[0].Text.Text)
	}
	return nil
}

func (f *fakeConversations) CloseConversation(id types.ConversationMeta) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id.ConversationID)
	f.active[id.ConversationID] = false
}

func (f *fakeConversations) Active(conversationID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[conversationID]
}

// complete signals to the scheduler that a conversation's turn has finished,
// mimicking the router fanning out a PromptResponse.
func (s *Scheduler) complete(meta types.ConversationMeta) {
	s.Receive(context.Background(), nil, meta, acp.PromptResponse{})
}

func TestScheduler_UpsertProject(t *testing.T) {
	store := db.NewMemStore()
	cfg := &config.Config{
		ScheduledJobs: []config.ScheduledJob{
			{
				Name:    "test-job",
				Dir:     "/tmp/test",
				Agent:   "test-agent",
				Sandbox: "none",
				Hooks:   "commit",
			},
		},
	}

	s := NewScheduler(nil, cfg, store)
	err := s.upsertProject(context.Background(), cfg.ScheduledJobs[0])
	require.NoError(t, err)

	proj, err := store.GetProject(context.Background(), "test-job")
	require.NoError(t, err)
	require.Equal(t, "/tmp/test", proj.Dir)
	require.Equal(t, "test-agent", proj.Agent)
	require.Equal(t, "none", proj.Sandbox)
	require.Equal(t, "commit", proj.Hooks)
}

func TestScheduler_UpsertProjectEnv(t *testing.T) {
	store := db.NewMemStore()
	job := config.ScheduledJob{
		Name: "env-job",
		Dir:  "/tmp/test",
		Env:  []string{"FOO=bar", "BAZ=qux"},
	}

	s := NewScheduler(nil, &config.Config{}, store)
	err := s.upsertProject(context.Background(), job)
	require.NoError(t, err)

	proj, err := store.GetProject(context.Background(), "env-job")
	require.NoError(t, err)
	require.Equal(t, []string{"FOO=bar", "BAZ=qux"}, proj.Env)
}

func TestScheduler_SkipOverlap(t *testing.T) {
	s := NewScheduler(nil, &config.Config{}, nil)

	// Simulate a running job.
	s.mu.Lock()
	s.running["test-job"] = true
	s.mu.Unlock()

	// runJob should skip without panic (overlap check happens before prompt read).
	s.runJob(config.ScheduledJob{Name: "test-job", Prompt: "/nonexistent"})

	// Verify it's still marked as running (skip path doesn't clear it).
	s.mu.Lock()
	require.True(t, s.running["test-job"])
	s.mu.Unlock()
}

func TestScheduler_RunJob(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "test.md")
	require.NoError(t, os.WriteFile(promptFile, []byte("Hello from test"), 0644))

	conv := newFakeConversations()
	cfg := &config.Config{Defaults: config.Defaults{Agent: "stub"}}
	s := NewScheduler(conv, cfg, db.NewMemStore())

	job := config.ScheduledJob{Name: "prompt-test", Prompt: promptFile, Dir: dir}
	s.runJob(job)

	// A conversation was created and the prompt submitted; the job stays running
	// (and the conversation open) until the turn's PromptResponse arrives.
	require.Len(t, conv.created, 1)
	require.Equal(t, "prompt-test", conv.created[0].ProjectID)
	require.Equal(t, []string{"Hello from test"}, conv.submitted)
	require.Empty(t, conv.closed)
	s.mu.Lock()
	require.True(t, s.running["prompt-test"])
	s.mu.Unlock()

	// Completion closes the (non-reuse) conversation and frees the job.
	s.complete(conv.created[0])
	require.Len(t, conv.closed, 1)
	s.mu.Lock()
	require.False(t, s.running["prompt-test"])
	s.mu.Unlock()
}

func TestScheduler_RunJobMissingPromptFile(t *testing.T) {
	conv := newFakeConversations()
	cfg := &config.Config{Defaults: config.Defaults{Agent: "stub"}}
	s := NewScheduler(conv, cfg, db.NewMemStore())

	s.runJob(config.ScheduledJob{Name: "missing-prompt", Prompt: "/nonexistent/path.md", Dir: t.TempDir()})

	// No conversation created, no prompt sent, and the job is freed.
	require.Empty(t, conv.created)
	require.Empty(t, conv.submitted)
	s.mu.Lock()
	require.False(t, s.running["missing-prompt"])
	s.mu.Unlock()
}

func TestScheduler_RunJobClearsRunningFlag(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "test.md")
	require.NoError(t, os.WriteFile(promptFile, []byte("test"), 0644))

	conv := newFakeConversations()
	cfg := &config.Config{Defaults: config.Defaults{Agent: "stub"}}
	s := NewScheduler(conv, cfg, db.NewMemStore())

	job := config.ScheduledJob{Name: "flag-test", Prompt: promptFile, Dir: dir}
	s.runJob(job)
	s.complete(conv.created[0])

	// After the turn completes, the running flag should be cleared.
	s.mu.Lock()
	require.False(t, s.running["flag-test"])
	s.mu.Unlock()
}

func TestScheduler_RunJobClosesConversation(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "test.md")
	require.NoError(t, os.WriteFile(promptFile, []byte("test prompt"), 0644))

	conv := newFakeConversations()
	cfg := &config.Config{Defaults: config.Defaults{Agent: "stub"}}
	s := NewScheduler(conv, cfg, db.NewMemStore())

	s.runJob(config.ScheduledJob{Name: "exit-test", Prompt: promptFile, Dir: dir})
	s.complete(conv.created[0])

	// The non-reuse job must close its own conversation so it does not linger.
	require.Len(t, conv.closed, 1)
	require.False(t, conv.Active(conv.closed[0]), "job conversation should have been closed")
}

func TestScheduler_ReuseSession(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "test.md")
	require.NoError(t, os.WriteFile(promptFile, []byte("reuse prompt"), 0644))

	conv := newFakeConversations()
	cfg := &config.Config{Defaults: config.Defaults{Agent: "stub"}}
	s := NewScheduler(conv, cfg, db.NewMemStore())

	job := config.ScheduledJob{Name: "reuse-test", Prompt: promptFile, Dir: dir, ReuseSession: true}

	// First run creates a conversation; second run reuses it.
	s.runJob(job)
	s.complete(conv.created[0])
	s.runJob(job)
	s.complete(conv.created[0])

	require.Len(t, conv.created, 1, "second run should reuse the conversation")
	require.Len(t, conv.submitted, 2)
	require.Empty(t, conv.closed, "reuse jobs must not close their conversation")
}

func TestScheduler_ReuseSessionRecreatesAfterClose(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "test.md")
	require.NoError(t, os.WriteFile(promptFile, []byte("test"), 0644))

	conv := newFakeConversations()
	cfg := &config.Config{Defaults: config.Defaults{Agent: "stub"}}
	s := NewScheduler(conv, cfg, db.NewMemStore())

	job := config.ScheduledJob{Name: "recreate-test", Prompt: promptFile, Dir: dir, ReuseSession: true}

	s.runJob(job)
	first := conv.created[0]
	s.complete(first)

	// Close the conversation externally (simulating idle cleanup, etc.).
	conv.CloseConversation(first)

	// Second run should detect the closed conversation and create a new one.
	s.runJob(job)
	require.Len(t, conv.created, 2)
	require.NotEqual(t, first.ConversationID, conv.created[1].ConversationID)
}

func TestScheduler_NoRouter(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "test.md")
	require.NoError(t, os.WriteFile(promptFile, []byte("test"), 0644))

	cfg := &config.Config{Defaults: config.Defaults{Agent: "stub"}}

	// No conversations backend — runJob must return early without panic.
	s := NewScheduler(nil, cfg, db.NewMemStore())
	s.runJob(config.ScheduledJob{Name: "no-router-test", Prompt: promptFile, Dir: dir})

	// And the running flag is still cleared.
	s.mu.Lock()
	require.False(t, s.running["no-router-test"])
	s.mu.Unlock()
}
