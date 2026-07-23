package arena

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/permission"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/sandbox"
	"github.com/elek/acpp/types"
)

// Options configures an arena run.
type Options struct {
	// OutputDir is the base directory; the run lives in <OutputDir>/<plan.name>.
	OutputDir string
}

// layout holds the resolved on-disk paths for one arena directory.
type layout struct {
	base      string
	resources string
	runRoot   string
	outputs   string
	evalRoot  string
	metaPath  string
	reportOut string
}

func newLayout(outputDir, name string) layout {
	base := filepath.Join(outputDir, name)
	return layout{
		base:      base,
		resources: filepath.Join(base, "resources"),
		runRoot:   filepath.Join(base, "run"),
		outputs:   filepath.Join(base, "output"),
		evalRoot:  filepath.Join(base, "eval"),
		metaPath:  filepath.Join(base, "meta.yaml"),
		reportOut: filepath.Join(base, "report.md"),
	}
}

// Run executes the plan: fetches resources, runs each contestant, has each
// evaluate every result, and writes report.md. It is idempotent — completed
// runs and evaluations on disk are reused; only gaps are filled.
func Run(ctx context.Context, planPath string, opts Options) error {
	absPlan, err := filepath.Abs(planPath)
	if err != nil {
		return err
	}
	plan, err := LoadPlan(absPlan)
	if err != nil {
		return err
	}
	planDir := filepath.Dir(absPlan)

	// Resolve the output dir to an absolute path: the run/eval working dirs
	// derived from it are handed to the sandbox as bind mounts, and bwrap
	// requires absolute paths (a relative cwd bind resolves against bwrap's own
	// working directory and fails).
	absOut, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return err
	}

	lo := newLayout(absOut, plan.Name)
	if err := os.MkdirAll(lo.base, 0o755); err != nil {
		return err
	}

	// Fail fast if the evaluator needs bwrap but it is unavailable.
	evalSandbox := plan.Evaluation.Sandbox
	if evalSandbox == "" {
		evalSandbox = "bbwrap"
	}
	if evalSandbox != "none" {
		if err := sandbox.LookupBwrap(); err != nil {
			return fmt.Errorf("arena: evaluation sandbox %q requires bwrap: %w", evalSandbox, err)
		}
	}

	// Reconcile the blind id map and persist it before doing any work.
	meta, err := LoadMeta(lo.metaPath)
	if err != nil {
		return err
	}
	meta.Reconcile(plan, uuid.NewString)
	if err := meta.Save(lo.metaPath); err != nil {
		return err
	}

	if err := fetchResources(ctx, plan, planDir, lo.resources); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	rt := router.New(router.WithConfig(cfg))
	defer rt.Close()
	permission.NewAllowAll(rt)

	a := &arena{rt: rt, coll: newCollector(), plan: plan, meta: meta, lo: lo, evalSandbox: evalSandbox}
	// Subscribe the collector before creating any conversation so it never misses
	// a turn's terminal event.
	rt.Subscribe(a.coll.Receive)

	a.executeRuns(ctx)
	a.evaluate(ctx)
	return a.writeReport()
}

type arena struct {
	rt          *router.Router
	coll        *collector
	plan        *Plan
	meta        *Meta
	lo          layout
	evalSandbox string

	mu         sync.Mutex
	incomplete []string
}

func (a *arena) note(format string, args ...any) {
	a.mu.Lock()
	a.incomplete = append(a.incomplete, fmt.Sprintf(format, args...))
	a.mu.Unlock()
}

// executeRuns runs every contestant whose output is not already on disk. Runs
// execute serially so their sandboxes never contend for shared resources.
func (a *arena) executeRuns(ctx context.Context) {
	for _, r := range a.meta.Runs {
		outDir := filepath.Join(a.lo.outputs, r.ID)
		if fileExists(filepath.Join(outDir, "response.md")) {
			continue // already complete
		}
		if err := a.executeRun(ctx, r, outDir); err != nil {
			a.note("run %q (%s) failed: %v", r.Name, r.ID, err)
		}
	}
}

func (a *arena) executeRun(ctx context.Context, r RunMeta, outDir string) error {
	runDir := filepath.Join(a.lo.runRoot, r.ID)
	// Fresh copy of the resources as the working tree.
	if err := os.RemoveAll(runDir); err != nil {
		return err
	}
	if err := copyDir(a.lo.resources, runDir); err != nil {
		return fmt.Errorf("seed run dir: %w", err)
	}

	res, err := a.prompt(ctx, promptSpec{
		cwd:    runDir,
		agent:  r.Agent,
		prompt: a.plan.Prompt,
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if _, err := collectArtifacts(runDir, outDir, a.plan.Artifacts); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "conversation.jsonl"),
		[]byte(strings.Join(bytesToLines(res.raw), "\n")), 0o644); err != nil {
		return err
	}
	// Write response.md last: its presence is the completion marker.
	return os.WriteFile(filepath.Join(outDir, "response.md"), []byte(res.text), 0o644)
}

// evaluate runs every evaluator whose scores.json is missing or invalid.
func (a *arena) evaluate(ctx context.Context) {
	// Bind only the outputs that actually exist.
	var roBinds []string
	var wantIDs []string
	absResources, _ := filepath.Abs(a.lo.resources)
	if resolved, err := filepath.EvalSymlinks(absResources); err == nil {
		absResources = resolved
	}
	roBinds = append(roBinds, absResources+":/resources")
	for _, id := range a.meta.RunIDs() {
		outDir := filepath.Join(a.lo.outputs, id)
		if !isNonEmptyDir(outDir) {
			continue
		}
		abs, _ := filepath.Abs(outDir)
		roBinds = append(roBinds, abs+":/outputs/"+id)
		wantIDs = append(wantIDs, id)
	}
	if len(wantIDs) == 0 {
		a.note("no contestant outputs available; skipped evaluation")
		return
	}

	evalPrompt := a.plan.Evaluation.Prompt
	if evalPrompt == "" {
		evalPrompt = defaultEvalPrompt(len(wantIDs))
	}

	// Evaluators run serially so their sandboxes never contend for shared
	// resources.
	for _, e := range a.meta.Evaluations {
		scoresPath := filepath.Join(a.lo.evalRoot, e.ID, "scores.json")
		if data, err := os.ReadFile(scoresPath); err == nil {
			if _, err := ParseScores(data, wantIDs); err == nil {
				continue // already valid
			}
		}
		if err := a.evaluateOne(ctx, e, evalPrompt, roBinds, wantIDs); err != nil {
			a.note("evaluator %q (%s) failed: %v", e.Name, e.ID, err)
		}
	}
}

func (a *arena) evaluateOne(ctx context.Context, e EvalMeta, evalPrompt string, roBinds, wantIDs []string) error {
	evalDir := filepath.Join(a.lo.evalRoot, e.ID)
	if err := os.MkdirAll(evalDir, 0o755); err != nil {
		return err
	}
	agent := ""
	for _, r := range a.meta.Runs {
		if r.Name == e.Name {
			agent = r.Agent
		}
	}
	if agent == "" {
		return fmt.Errorf("no agent command for evaluator %q", e.Name)
	}

	if _, err := a.prompt(ctx, promptSpec{
		cwd:         evalDir,
		agent:       agent,
		prompt:      evalPrompt,
		sandboxType: a.evalSandbox,
		profiles:    a.plan.Evaluation.Profiles,
		roBinds:     roBinds,
	}); err != nil {
		return err
	}

	data, err := os.ReadFile(filepath.Join(evalDir, "scores.json"))
	if err != nil {
		return fmt.Errorf("scores.json not produced: %w", err)
	}
	if _, err := ParseScores(data, wantIDs); err != nil {
		return err
	}
	return nil
}

// writeReport reads every evaluator's scores.json and renders report.md.
func (a *arena) writeReport() error {
	wantIDs := a.meta.RunIDs()
	results := make(map[string][]Score)
	for _, e := range a.meta.Evaluations {
		data, err := os.ReadFile(filepath.Join(a.lo.evalRoot, e.ID, "scores.json"))
		if err != nil {
			continue
		}
		// Validate against whatever outputs exist, not the full set.
		scores, err := ParseScores(data, presentIDs(a.lo.outputs, wantIDs))
		if err != nil {
			continue
		}
		results[e.Name] = scores
	}
	report := BuildReport(a.meta, results, a.incomplete)
	return os.WriteFile(a.lo.reportOut, []byte(report), 0o644)
}

// promptSpec describes one prompt turn to run through the router.
type promptSpec struct {
	cwd         string
	agent       string
	prompt      string
	sandboxType string
	profiles    string
	roBinds     []string
}

// promptResult is the collected output of one turn.
type promptResult struct {
	text string
	raw  [][]byte
}

// prompt drives one full turn: create → wait ready → send → await terminal, then
// closes the conversation.
func (a *arena) prompt(ctx context.Context, spec promptSpec) (promptResult, error) {
	id, err := a.rt.Create(ctx, types.SessionOpts{
		ProjectID:       spec.cwd,
		Agent:           spec.agent,
		CWD:             spec.cwd,
		Source:          "arena",
		SandboxType:     spec.sandboxType,
		SandboxProfiles: spec.profiles,
		ROBinds:         spec.roBinds,
	})
	if err != nil {
		return promptResult{}, fmt.Errorf("create: %w", err)
	}
	buf := a.coll.register(id.ConversationID)
	defer a.rt.CloseConversation(id)

	// Abort the turn if the conversation reaches a terminal event: a startup
	// crash (ConversationClosed before the handshake) would otherwise leave
	// WaitReady blocking until the parent context is cancelled.
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-buf.done:
			cancel()
		case <-turnCtx.Done():
		}
	}()

	result := func() promptResult {
		buf.mu.Lock()
		defer buf.mu.Unlock()
		return promptResult{text: buf.text.String(), raw: buf.raw}
	}

	// closedEarly reports the error from a terminal event that fired before the
	// turn completed normally (e.g. a crash during startup or mid-turn).
	closedEarly := func(op string, err error) error {
		select {
		case <-buf.done:
			t := buf.terminalEvent()
			if t.err != "" {
				return fmt.Errorf("conversation ended with error: %s", t.err)
			}
			return fmt.Errorf("conversation closed before turn completed (%s)", op)
		default:
			return fmt.Errorf("%s: %w", op, err)
		}
	}

	meta, err := a.rt.WaitReady(turnCtx, id)
	if err != nil {
		return promptResult{}, closedEarly("wait ready", err)
	}
	if err := a.rt.Send(turnCtx, meta, acp.PromptRequest{
		SessionId: meta.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(spec.prompt)},
	}); err != nil {
		return promptResult{}, closedEarly("send", err)
	}

	select {
	case <-buf.done:
		t := buf.terminalEvent()
		res := result()
		if t.err != "" {
			return res, fmt.Errorf("conversation ended with error: %s", t.err)
		}
		return res, nil
	case <-ctx.Done():
		return promptResult{}, ctx.Err()
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// presentIDs returns the subset of ids whose output dir exists and is non-empty.
func presentIDs(outputRoot string, ids []string) []string {
	var out []string
	for _, id := range ids {
		if isNonEmptyDir(filepath.Join(outputRoot, id)) {
			out = append(out, id)
		}
	}
	return out
}

func bytesToLines(raw [][]byte) []string {
	out := make([]string, len(raw))
	for i, b := range raw {
		out[i] = string(b)
	}
	return out
}
