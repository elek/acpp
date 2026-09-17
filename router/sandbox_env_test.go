package router

import (
	"context"
	"testing"

	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// sandboxFor resolves the sandbox for a project through the same path
// Router.Create uses, without spawning anything.
func sandboxFor(t *testing.T, r *Router, project string) types.SessionOpts {
	t.Helper()
	opts := types.SessionOpts{ProjectID: project, CWD: t.TempDir(), SandboxType: "bbwrap"}
	_, settings, err := r.resolveProject(context.Background(), &opts)
	require.NoError(t, err)
	require.NoError(t, r.resolveSandbox(&opts, settings))
	require.NotNil(t, opts.Sandbox)
	return opts
}

func TestProjectSandboxEnvDefaultWhitelist(t *testing.T) {
	r, project := projectRouter(t, nil)
	opts := sandboxFor(t, r, project)

	got := opts.Sandbox.FilterEnv([]string{"PATH=/bin", "AWS_SECRET_ACCESS_KEY=hunter2"})
	require.Equal(t, []string{"PATH=/bin"}, got)
}

func TestProjectSandboxEnvAllowsExtraNames(t *testing.T) {
	r, project := projectRouter(t, map[string]string{"sandbox_env": "GITHUB_TOKEN, GH_HOST"})
	opts := sandboxFor(t, r, project)

	got := opts.Sandbox.FilterEnv([]string{"PATH=/bin", "GITHUB_TOKEN=ghp", "GH_HOST=x", "NPM_TOKEN=n"})
	require.ElementsMatch(t, []string{"PATH=/bin", "GITHUB_TOKEN=ghp", "GH_HOST=x"}, got)
}

func TestProjectSandboxEnvCanDenyDefault(t *testing.T) {
	r, project := projectRouter(t, map[string]string{"sandbox_env": "-ANTHROPIC_*"})
	opts := sandboxFor(t, r, project)

	got := opts.Sandbox.FilterEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=sk"})
	require.Equal(t, []string{"PATH=/bin"}, got)
}
