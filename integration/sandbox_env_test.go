package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elek/acpp/sandbox"
	"github.com/stretchr/testify/require"
)

// TestSandboxEnvWhitelistE2E is the end-to-end proof that the environment
// whitelist actually holds against real bwrap, rather than only in the unit
// tests' view of the argument list: it runs /usr/bin/env inside a sandbox built
// the way a session's is, and inspects what the process really sees.
//
// It uses a minimal bind set written to a temp config rather than the embedded
// one, so the test is about the environment alone and does not fail on a host
// missing some /etc file the default fragments bind.
//
// Gated on bwrap. Note it cannot pass from inside an acpp session: bwrap will
// not nest inside the userns the outer sandbox already created.
func TestSandboxEnvWhitelistE2E(t *testing.T) {
	requireTool(t, "bwrap")

	configPath := filepath.Join(t.TempDir(), "bbwrap.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
sandbox:
  ro-bind:
    - /usr
    - /etc
    - /bin
    - /lib
    - /lib64
`), 0o644))

	// A config dir with no bbwrap.yaml, so the developer's own override does not
	// change what this test builds.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Setenv("AWS_SECRET_ACCESS_KEY", "hunter2")
	t.Setenv("GITHUB_TOKEN", "ghp_leak")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-kept")
	t.Setenv("LANG", "en_US.UTF-8")

	sb, err := sandbox.ResolveSandbox("bbwrap", "", "/tmp", nil, nil, nil, configPath)
	require.NoError(t, err)

	name, args := sb.Wrap("/usr/bin/env", nil)
	cmd := exec.Command(name, args...)
	cmd.Dir = "/tmp"
	cmd.Env = sb.FilterEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "bwrap output: %s", out)

	got := string(out)
	require.NotContains(t, got, "AWS_SECRET_ACCESS_KEY", "host secret leaked into the sandbox")
	require.NotContains(t, got, "GITHUB_TOKEN", "host secret leaked into the sandbox")

	for _, want := range []string{"PATH=", "HOME=", "USER=", "LANG=en_US.UTF-8", "ANTHROPIC_API_KEY=sk-ant-kept", "TMPDIR=/tmp"} {
		require.Contains(t, got, want)
	}

	// The whitelist is enforced by narrowing the bwrap process's own environment,
	// not by --setenv, precisely so secrets stay out of a world-readable argv.
	require.NotContains(t, strings.Join(args, " "), "sk-ant-kept",
		"API key must not appear in the bwrap command line")
}
