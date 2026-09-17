package process

import (
	"os"
	"testing"

	"github.com/elek/acpp/sandbox"
	"github.com/stretchr/testify/require"
)

// filterSandbox is a Sandbox that only lets the named variables through.
type filterSandbox struct{ allow map[string]bool }

func (f filterSandbox) Wrap(command string, args []string) (string, []string) {
	return command, args
}

func (f filterSandbox) FilterEnv(hostEnv []string) []string {
	var out []string
	for _, entry := range hostEnv {
		for name := range f.allow {
			if len(entry) > len(name) && entry[:len(name)+1] == name+"=" {
				out = append(out, entry)
			}
		}
	}
	return out
}

func TestBuildEnvWithoutSandboxKeepsHostEnv(t *testing.T) {
	t.Setenv("ACPP_TEST_SECRET", "leak")

	env := buildEnv(Spec{Env: []string{"EXPLICIT=1"}})

	require.Contains(t, env, "ACPP_TEST_SECRET=leak")
	require.Contains(t, env, "EXPLICIT=1")
	require.Len(t, env, len(os.Environ())+1)
}

func TestBuildEnvSandboxFiltersHostEnv(t *testing.T) {
	t.Setenv("ACPP_TEST_SECRET", "leak")
	t.Setenv("ACPP_TEST_KEEP", "ok")

	env := buildEnv(Spec{Sandbox: filterSandbox{allow: map[string]bool{"ACPP_TEST_KEEP": true}}})

	require.Equal(t, []string{"ACPP_TEST_KEEP=ok"}, env)
}

// A project's explicit env entries are deliberate configuration, so they must
// survive the whitelist even when their name is not in it.
func TestBuildEnvExplicitEnvSurvivesFilter(t *testing.T) {
	env := buildEnv(Spec{
		Sandbox: filterSandbox{allow: map[string]bool{}},
		Env:     []string{"GITHUB_TOKEN=ghp_explicit"},
	})

	require.Equal(t, []string{"GITHUB_TOKEN=ghp_explicit"}, env)
}

// The none sandbox must not filter: "none" means no isolation at all.
func TestBuildEnvNoneSandboxKeepsHostEnv(t *testing.T) {
	t.Setenv("ACPP_TEST_SECRET", "leak")

	env := buildEnv(Spec{Sandbox: sandbox.NewNoneSandbox()})

	require.Contains(t, env, "ACPP_TEST_SECRET=leak")
}
