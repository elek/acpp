package arena

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestArenaAgentCrashDoesNotHang verifies that a contestant command which exits
// immediately without speaking ACP is finalized (not left blocking WaitReady):
// arena must return promptly and record the failure rather than hang.
func TestArenaAgentCrashDoesNotHang(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.yaml")
	require.NoError(t, os.WriteFile(planPath, []byte(`
name: crash
prompt: do the task
agents:
  - { name: dead, agent: "true" }
evaluation:
  sandbox: none
`), 0o644))

	out := filepath.Join(dir, "out")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx, planPath, Options{OutputDir: out}) }()
	select {
	case err := <-done:
		require.NoError(t, err, "arena should complete even when an agent crashes")
	case <-ctx.Done():
		t.Fatal("arena hung on a crashing agent")
	}

	report, err := os.ReadFile(filepath.Join(out, "crash", "report.md"))
	require.NoError(t, err)
	require.Contains(t, string(report), "## Incomplete")
	require.Contains(t, string(report), `run "dead"`)
}

// TestArenaEndToEnd runs a real two-contestant arena against the rai acp fake
// agent subprocess: it exercises resource fetch+copy, parallel execution
// through the router, output archiving, idempotent resume, evaluator score
// pickup, and report generation. It is skipped when rai is not installed.
//
// The fake agent cannot itself write a valid scores.json, so the evaluators'
// scores are pre-seeded before the second (resume) run — which also verifies
// that a valid scores.json on disk short-circuits the evaluation round.
func TestArenaEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("rai"); err != nil {
		t.Skip("rai agent not installed; skipping arena e2e")
	}
	// Isolate config so the run uses defaults (no sandbox) regardless of the
	// developer's ~/.config/acpp.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	dir := t.TempDir()
	fixtures := filepath.Join(dir, "fixtures")
	require.NoError(t, os.MkdirAll(fixtures, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fixtures, "task.txt"), []byte("fix it"), 0o644))

	planPath := filepath.Join(dir, "plan.yaml")
	require.NoError(t, os.WriteFile(planPath, []byte(`
name: e2e
resources:
  - ./fixtures
prompt: summarize task.txt
agents:
  - { name: alpha, agent: rai acp fake }
  - { name: beta, agent: rai acp fake }
evaluation:
  sandbox: none
`), 0o644))

	out := filepath.Join(dir, "out")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	require.NoError(t, Run(ctx, planPath, Options{OutputDir: out}))

	base := filepath.Join(out, "e2e")
	meta, err := LoadMeta(filepath.Join(base, "meta.yaml"))
	require.NoError(t, err)
	require.Len(t, meta.Runs, 2)
	require.Len(t, meta.Evaluations, 2)

	// Every contestant produced an archived response, and its run dir was seeded
	// with an independent copy of the resources.
	responseHashes := map[string]string{}
	for _, r := range meta.Runs {
		resp := filepath.Join(base, "output", r.ID, "response.md")
		require.FileExists(t, resp)
		require.FileExists(t, filepath.Join(base, "run", r.ID, "task.txt"))
		data, err := os.ReadFile(resp)
		require.NoError(t, err)
		responseHashes[r.ID] = string(data)
	}

	// Pre-seed valid scores so the resume run's evaluation round is a no-op and
	// the report has real numbers. Each evaluator gives every contestant a 5.
	for _, e := range meta.Evaluations {
		evalDir := filepath.Join(base, "eval", e.ID)
		require.NoError(t, os.MkdirAll(evalDir, 0o755))
		var scores []Score
		for _, r := range meta.Runs {
			scores = append(scores, Score{ID: r.ID, Score: 5, Rationale: "seeded"})
		}
		data, err := json.Marshal(scores)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(evalDir, "scores.json"), data, 0o644))
	}

	// Second run: idempotent resume.
	require.NoError(t, Run(ctx, planPath, Options{OutputDir: out}))

	// Completed runs were not re-executed (fake output is random, so identical
	// content proves the run was skipped).
	for id, want := range responseHashes {
		got, err := os.ReadFile(filepath.Join(base, "output", id, "response.md"))
		require.NoError(t, err)
		require.Equal(t, want, string(got), "run %s must not be re-executed on resume", id)
	}

	// Report reflects the seeded scores: each contestant scored 5 by 2 evaluators.
	report, err := os.ReadFile(filepath.Join(base, "report.md"))
	require.NoError(t, err)
	text := string(report)
	require.Contains(t, text, "# Arena report: e2e")
	require.Contains(t, text, "| alpha | 10 |")
	require.Contains(t, text, "| beta | 10 |")
	require.NotContains(t, text, "## Incomplete", "no failures expected on the seeded resume run")
	require.Contains(t, text, "| evaluator \\ contestant | alpha | beta |")
	// Sanity: the alpha evaluator row has a numeric cell.
	require.True(t, strings.Contains(text, "| alpha | 5 | 5 |"), "pivot row for alpha: %s", text)
}
