package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elek/acpp/arena"
	"github.com/elek/acpp/sandbox"
	"github.com/stretchr/testify/require"
)

// TestArenaSandboxedSmoke runs the arena plan in integration/testdata/arena.yaml
// end to end with the execution AND evaluation rounds wrapped in a bubblewrap
// sandbox. It is the regression guard for "the sandboxes failed": a broken or
// missing sandbox profile makes every agent die before its turn, which would
// leave no output and surface as a "bwrap:"/"exited before completing" error.
//
// To stay hermetic it uses the rai acp fake agent (no credentials/network) under
// a clean, self-contained sandbox profile written to an isolated XDG_CONFIG_HOME
// (so the developer's own ~/.config/acpp profile — which may reference paths that
// do not exist on this machine — is never consulted). The fake agent cannot
// itself write a valid scores.json, so the assertions focus on what the sandbox
// governs: every contestant must actually run and be archived, and each
// evaluator must launch and complete its turn (failing only on the missing
// scores.json, never on the sandbox).
func TestArenaSandboxedSmoke(t *testing.T) {
	raiPath, err := exec.LookPath("rai")
	if err != nil {
		t.Skip("rai agent not installed; skipping arena sandbox smoke")
	}
	if err := sandbox.LookupBwrap(); err != nil {
		t.Skip("bwrap not available; skipping arena sandbox smoke")
	}

	// Isolate config so the run cannot inherit the developer's global sandbox
	// profile, and give it a clean bbwrap profile that binds only what the fake
	// agent needs (its binary directory, and its config dir if present).
	cfgDir := t.TempDir()
	writeIsolatedSandboxConfig(t, cfgDir, filepath.Dir(raiPath))
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	// Use a RELATIVE output dir from a temp working directory. This guards the
	// regression where a relative run-dir path was handed to bwrap verbatim and
	// failed to bind ("Can't find source path smoketest/run/..."). The plan path
	// is made absolute since the cwd changes.
	planPath, err := filepath.Abs("testdata/arena.yaml")
	require.NoError(t, err)
	work := t.TempDir()
	t.Chdir(work)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	require.NoError(t, arena.Run(ctx, planPath, arena.Options{OutputDir: "out"}))

	base := filepath.Join(work, "out", "smoketest")
	meta, err := arena.LoadMeta(filepath.Join(base, "meta.yaml"))
	require.NoError(t, err)
	require.Len(t, meta.Runs, 2)
	require.Len(t, meta.Evaluations, 2)

	// Every contestant executed inside the sandbox and was archived. If the
	// sandbox had failed, these files would not exist.
	for _, r := range meta.Runs {
		resp := filepath.Join(base, "output", r.ID, "response.md")
		require.FileExists(t, resp, "contestant %q produced no output — sandbox likely failed", r.Name)
	}

	report, err := os.ReadFile(filepath.Join(base, "report.md"))
	require.NoError(t, err)
	text := string(report)

	// The sandbox must not have failed for anyone: no bwrap errors, and no agent
	// died before finishing its turn.
	require.NotContains(t, text, "bwrap:", "sandbox reported a bwrap error:\n%s", text)
	require.NotContains(t, text, "exited before completing",
		"an agent died before its turn — sandbox likely failed:\n%s", text)

	// The evaluators launched under bbwrap (with the read-only /resources and
	// /outputs binds) and completed their turn; the only expected failure is that
	// the fake agent does not write scores.json.
	require.Contains(t, text, "scores.json not produced",
		"expected evaluators to run but not produce scores under the fake agent:\n%s", text)
}

// writeIsolatedSandboxConfig writes an acpp config + a clean bbwrap profile into
// cfgDir/acpp. The profile extends the embedded base+etc fragments (system libs,
// certs, etc.) and additionally binds agentDir so the sandboxed agent binary is
// reachable; it binds ~/.config/rai read-write when present.
func writeIsolatedSandboxConfig(t *testing.T, cfgDir, agentDir string) {
	t.Helper()
	acppDir := filepath.Join(cfgDir, "acpp")
	require.NoError(t, os.MkdirAll(acppDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(acppDir, "config.yaml"),
		[]byte("defaults:\n  sandbox: bbwrap\n"), 0o644))

	var binds strings.Builder
	fmt.Fprintf(&binds, "sandbox:\n  extend: [base, etc]\n  ro-bind:\n    - %s\n", agentDir)
	if home, err := os.UserHomeDir(); err == nil {
		raiCfg := filepath.Join(home, ".config", "rai")
		if st, err := os.Stat(raiCfg); err == nil && st.IsDir() {
			fmt.Fprintf(&binds, "  bind:\n    - %s\n", raiCfg)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(acppDir, "bbwrap.yaml"), []byte(binds.String()), 0o644))
}
