// Package tck is a Test Compatibility Kit for ACP agent binaries. It runs a
// fixed scenario of real prompts against an agent, taps the router to observe
// all protocol traffic, and evaluates a set of checks that report compatibility
// properties (advertised capabilities, available commands, usage updates, tool
// usage, prompt completion, …).
package tck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/permission"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
)

// Probe is a single prompt in the scenario, tagged so checks can find its turn.
type Probe struct {
	Tag    string
	Prompt string
}

// scenario returns the ordered probe prompts for the first session. secret is the
// unique token the memo probe plants, which the resume phase later asks for.
func scenario(secret string) []Probe {
	return []Probe{
		{Tag: "memo", Prompt: fmt.Sprintf(
			"Remember this exact token, I will ask you for it later: %s. Reply with just OK.", secret)},
		{Tag: "capital", Prompt: "What is the capital of Spain? Answer in one word."},
		{Tag: "list-dir", Prompt: "List the names of the files in the current working directory."},
	}
}

// recallProbe is the prompt sent after the session is resumed. It can only be
// answered from the restored conversation context: the token was never written
// anywhere the agent can look up in the working directory.
const recallProbe = "Earlier in this conversation I asked you to remember a token. " +
	"Reply with that token and nothing else."

// Runner tests a single agent binary.
type Runner struct {
	Agent   string
	Timeout time.Duration
}

// Run executes the scenario against the agent and returns the check results. It
// creates an isolated temp working directory with a uniquely-named probe file,
// drives the full session lifecycle through the router, then evaluates checks
// over the collected transcript. The agent subprocess is always torn down before
// Run returns.
//
// The run has two phases. The first drives a fresh session through the scenario
// probes. The second stops that session (killing its subprocess) and starts a new
// one that resumes the same ACP session id via session/load, then asks for a token
// only the first session's context can supply — so a passing resume check means
// the agent really restored the conversation rather than merely accepting the
// load request.
func (r *Runner) Run(ctx context.Context) ([]Result, error) {
	dir, err := os.MkdirTemp("", "acpp-tck-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	probeFile := fmt.Sprintf("acpp-tck-%d-%d.txt", os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, probeFile), []byte("tck probe file\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write probe file: %w", err)
	}

	secret, err := newSecret()
	if err != nil {
		return nil, err
	}

	rt := router.New()
	defer rt.Close()

	// Auto-approve permission requests and answer fs read/write callbacks so the
	// agent never blocks waiting on the client.
	permission.NewAllowAll(rt)
	ctrl := &control{rt: rt, dir: dir}
	tr := NewTranscript()
	tr.ProbeFile = probeFile
	tr.Secret = secret
	rt.Subscribe(tr.Record)
	rt.Subscribe(ctrl.handle)

	opts := types.SessionOpts{
		ProjectID: dir,
		Agent:     r.Agent,
		CWD:       dir,
		Source:    "tck",
	}
	meta, err := r.startSession(ctx, rt, opts)
	if err != nil {
		return nil, err
	}

	// The initialize response is not fanned out to subscribers; fetch it directly.
	if init, ok := rt.Init(meta.ConversationID); ok {
		tr.SetInit(init)
	}

	for _, probe := range scenario(secret) {
		tr.Begin(probe.Tag)
		if err := r.prompt(ctx, rt, ctrl, meta, probe.Prompt); err != nil {
			return nil, fmt.Errorf("probe %q: %w", probe.Tag, err)
		}
	}

	r.resume(ctx, rt, tr, ctrl, meta, opts)

	return RunChecks(tr), nil
}

// resume runs the second phase: stop the live session and start a fresh one that
// loads the same ACP session id, then send the recall probe. Failures are recorded
// on the transcript (so the resume checks report them) rather than returned — a
// failed resume must not discard the results already collected.
func (r *Runner) resume(ctx context.Context, rt *router.Router, tr *Transcript, ctrl *control, prev types.ConversationMeta, opts types.SessionOpts) {
	if !supportsLoadSession(tr.Init.AgentCapabilities) {
		tr.ResumeSkipped = "loadSession not advertised"
		return
	}
	if prev.SessionID == "" {
		tr.ResumeSkipped = "no session id to resume"
		return
	}

	// Stop the session: CloseConversation tears down the agent subprocess, so the
	// resumed session must come back from whatever the agent persisted.
	rt.CloseConversation(prev)

	// An agent replays the prior conversation as session/update notifications while
	// handling session/load. Park those in their own bucket so they are not
	// attributed to the last probe — and so the recall answer is judged on the
	// resumed turn alone, not on replayed history that quotes the token.
	tr.BeginSink("resume-replay")

	opts.ResumeSessionID = prev.SessionID
	meta, err := r.startSession(ctx, rt, opts)
	if err != nil {
		tr.ResumeErr = err.Error()
		return
	}

	tr.Begin("recall")
	if err := r.prompt(ctx, rt, ctrl, meta, recallProbe); err != nil {
		tr.ResumeErr = err.Error()
	}
}

// startSession creates a conversation and blocks until its handshake completes.
func (r *Runner) startSession(ctx context.Context, rt *router.Router, opts types.SessionOpts) (types.ConversationMeta, error) {
	id, err := rt.Create(ctx, opts)
	if err != nil {
		return types.ConversationMeta{}, fmt.Errorf("create conversation: %w", err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	meta, err := rt.WaitReady(readyCtx, id)
	if err != nil {
		return types.ConversationMeta{}, fmt.Errorf("handshake: %w", err)
	}
	return meta, nil
}

// prompt sends one prompt and waits for its turn to complete. A timeout is not an
// error: the turn is simply left without a response, which checkPromptFinished
// reports. Only a send failure or a cancelled context stops the run.
func (r *Runner) prompt(ctx context.Context, rt *router.Router, ctrl *control, meta types.ConversationMeta, text string) error {
	done := ctrl.expect()
	if err := rt.Send(ctx, meta, acp.PromptRequest{
		SessionId: meta.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
	}); err != nil {
		return err
	}
	select {
	case <-done:
	case <-time.After(r.Timeout):
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// newSecret mints the token the memo probe plants and the resume phase asks back.
// It is random per run so no agent can answer it from anything but the restored
// conversation.
func newSecret() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return "ACPP-TOKEN-" + strings.ToUpper(hex.EncodeToString(b)), nil
}

// supportsLoadSession reports whether the agent advertised the loadSession
// capability in its initialize response. Resuming an agent that did not is a
// guaranteed error, so the resume phase is skipped instead.
func supportsLoadSession(caps json.RawMessage) bool {
	if len(caps) == 0 {
		return false
	}
	var c struct {
		LoadSession bool `json:"loadSession"`
	}
	if err := json.Unmarshal(caps, &c); err != nil {
		return false
	}
	return c.LoadSession
}

// control is a router.Subscriber that handles client-side callbacks (fs read,
// fs write) and signals turn completion on each PromptResponse.
type control struct {
	rt  *router.Router
	dir string

	mu   sync.Mutex
	done chan struct{}
}

// expect arms a fresh completion channel for the next turn and returns it.
func (c *control) expect() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.done = make(chan struct{})
	return c.done
}

func (c *control) handle(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	switch m := msg.(type) {
	case acp.ReadTextFileRequest:
		content := ""
		if b, err := os.ReadFile(c.resolve(m.Path)); err == nil {
			content = string(b)
		}
		_ = c.rt.Respond(ctx, rid, id, acp.ReadTextFileResponse{Content: content})
	case acp.WriteTextFileRequest:
		_ = os.WriteFile(c.resolve(m.Path), []byte(m.Content), 0o644)
		_ = c.rt.Respond(ctx, rid, id, acp.WriteTextFileResponse{})
	case acp.PromptResponse:
		c.mu.Lock()
		d := c.done
		c.done = nil
		c.mu.Unlock()
		if d != nil {
			close(d)
		}
	}
}

// resolve maps an agent-supplied path to an absolute path under the working dir.
func (c *control) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.dir, path)
}
