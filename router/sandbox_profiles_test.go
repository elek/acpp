package router

import (
	"context"
	"strings"
	"testing"

	"github.com/elek/acpp/db"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// projectRouter builds a router backed by an in-memory store, seeded with the
// given project fields (column name -> value). Returns the router and the
// project name to put in SessionOpts.ProjectID.
func projectRouter(t *testing.T, fields map[string]string) (*Router, string) {
	t.Helper()
	store := db.NewMemStore()
	const name = "widgets"
	for field, value := range fields {
		require.NoError(t, store.SetProjectField(context.Background(), name, field, value))
	}
	r := New(WithProjects(store))
	t.Cleanup(r.Close)
	return r, name
}

// wrapArgs resolves the sandbox for opts via the same code path Router.Create
// uses (resolveProject folds in the stored project config) and returns the bwrap
// argument line the built sandbox would exec. It never spawns a subprocess, so it
// runs even where docker/bwrap are unavailable — the docker profile only needs to
// appear in the argument list, not actually bind.
func wrapArgs(t *testing.T, r *Router, opts *types.SessionOpts) string {
	t.Helper()
	_, sbType, profiles, err := r.resolveProject(context.Background(), opts)
	require.NoError(t, err)
	require.NoError(t, r.resolveSandbox(opts, sbType, profiles))
	require.NotNil(t, opts.Sandbox, "resolveSandbox should have built a sandbox")
	name, args := opts.Sandbox.Wrap("sh", []string{"-c", "true"})
	return name + " " + strings.Join(args, " ")
}

// TestProjectProfilesApplied is the regression guard for the bug where a
// caller-supplied sandbox (web defaults, scheduler jobs) caused the project's
// configured profiles to be silently dropped, so a session never got e.g. the
// docker socket even though `acpp sandbox bash` in the same directory did.
//
// The subtests cover each source of profiles: the project row (the reported
// scenario), caller-supplied strings (the scheduler scenario, and the line
// resolveProject previously hardcoded to ""), and a router with no store at all
// (acpp cat / acpp run).
func TestProjectProfilesApplied(t *testing.T) {
	t.Run("from project row, caller only sets type", func(t *testing.T) {
		r, project := projectRouter(t, map[string]string{
			"sandbox":          "bbwrap",
			"sandbox_profiles": "docker",
		})

		// Mirrors what the web channel passes: a default sandbox type, no
		// pre-built Sandbox, so the project row is folded in.
		opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, SandboxType: "bbwrap"}
		line := wrapArgs(t, r, &opts)

		require.Contains(t, line, "docker.sock",
			"the project's docker profile must reach the session sandbox")
		require.Equal(t, "docker", opts.SandboxProfiles)
	})

	t.Run("from project row with profiles but no sandbox type", func(t *testing.T) {
		// A project that sets only profiles, relying on the caller/default for
		// the sandbox type.
		r, project := projectRouter(t, map[string]string{"sandbox_profiles": "docker"})

		opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, SandboxType: "bbwrap"}
		line := wrapArgs(t, r, &opts)

		require.Contains(t, line, "docker.sock",
			"profiles without a stored sandbox type must still reach the session sandbox")
		require.Equal(t, "docker", opts.SandboxProfiles)
	})

	t.Run("project row profiles override caller profiles", func(t *testing.T) {
		r, project := projectRouter(t, map[string]string{"sandbox_profiles": "docker"})

		opts := types.SessionOpts{
			CWD: t.TempDir(), ProjectID: project,
			SandboxType: "bbwrap", SandboxProfiles: "ssh",
		}
		line := wrapArgs(t, r, &opts)

		require.Contains(t, line, "docker.sock", "the stored value wins over the caller's")
		require.Equal(t, "docker", opts.SandboxProfiles)
	})

	t.Run("from caller profiles, nothing stored", func(t *testing.T) {
		r, project := projectRouter(t, nil)

		// Mirrors a scheduled job with sandbox_profiles set. Before the fix the
		// caller's profiles were dropped here (resolveProject used a literal "").
		opts := types.SessionOpts{
			CWD: t.TempDir(), ProjectID: project,
			SandboxType: "bbwrap", SandboxProfiles: "docker",
		}
		line := wrapArgs(t, r, &opts)

		require.Contains(t, line, "docker.sock",
			"caller-supplied profiles must reach the session sandbox")
		require.Equal(t, "docker", opts.SandboxProfiles)
	})

	t.Run("from caller profiles with no project store", func(t *testing.T) {
		// acpp cat / acpp run build a router without a database; the caller's
		// profiles must still apply.
		r := New()
		t.Cleanup(r.Close)

		opts := types.SessionOpts{CWD: t.TempDir(), SandboxType: "bbwrap", SandboxProfiles: "docker"}
		line := wrapArgs(t, r, &opts)

		require.Contains(t, line, "docker.sock",
			"a router with no project store must still honor caller profiles")
		require.Equal(t, "docker", opts.SandboxProfiles)
	})

	t.Run("no profiles means no docker bind", func(t *testing.T) {
		r, project := projectRouter(t, map[string]string{"sandbox": "bbwrap"})

		opts := types.SessionOpts{CWD: t.TempDir(), ProjectID: project, SandboxType: "bbwrap"}
		line := wrapArgs(t, r, &opts)

		require.NotContains(t, line, "docker.sock",
			"a session without the docker profile must not get the docker socket")
	})
}
