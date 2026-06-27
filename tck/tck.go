// Package tck is a Test Compatibility Kit for ACP agent binaries. It runs a
// fixed scenario of real prompts against an agent, taps the router to observe
// all protocol traffic, and evaluates a set of checks that report compatibility
// properties (advertised capabilities, available commands, usage updates, tool
// usage, prompt completion, …).
package tck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// scenario returns the ordered probe prompts. probeFile is the unique filename
// pre-created in the working directory that the list-dir probe should surface.
func scenario(probeFile string) []Probe {
	return []Probe{
		{Tag: "capital", Prompt: "What is the capital of Spain? Answer in one word."},
		{Tag: "list-dir", Prompt: "List the names of the files in the current working directory."},
	}
}

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

	rt := router.New()
	defer rt.Close()

	// Auto-approve permission requests and answer fs read/write callbacks so the
	// agent never blocks waiting on the client.
	permission.NewAllowAll(rt)
	ctrl := &control{rt: rt, dir: dir}
	tr := NewTranscript()
	tr.ProbeFile = probeFile
	rt.Subscribe(tr.Record)
	rt.Subscribe(ctrl.handle)

	id, err := rt.Create(ctx, types.SessionOpts{
		ProjectID: dir,
		Agent:     r.Agent,
		CWD:       dir,
		Source:    "tck",
	})
	if err != nil {
		return nil, fmt.Errorf("create conversation: %w", err)
	}

	readyCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	meta, err := rt.WaitReady(readyCtx, id)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}

	// The initialize response is not fanned out to subscribers; fetch it directly.
	if init, ok := rt.Init(id.ConversationID); ok {
		tr.SetInit(init)
	}

	for _, probe := range scenario(probeFile) {
		tr.Begin(probe.Tag)
		done := ctrl.expect()
		if err := rt.Send(ctx, meta, acp.PromptRequest{
			SessionId: meta.SessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock(probe.Prompt)},
		}); err != nil {
			return nil, fmt.Errorf("send prompt %q: %w", probe.Tag, err)
		}
		select {
		case <-done:
		case <-time.After(r.Timeout):
			// Record the timeout as a missing response and move on.
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return RunChecks(tr), nil
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
