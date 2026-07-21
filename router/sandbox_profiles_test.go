package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// wrapArgs resolves the sandbox for opts via the same code path Router.Create
// uses (resolveProject folds in .acpp.yaml) and returns the bwrap argument line
// the built sandbox would exec. It never spawns a subprocess, so it runs even
// where docker/bwrap are unavailable — the docker profile only needs to appear
// in the argument list, not actually bind.
func wrapArgs(t *testing.T, opts *types.SessionOpts) string {
	t.Helper()
	r := New()
	t.Cleanup(r.Close)
	_, err := r.resolveProject(opts)
	require.NoError(t, err)
	require.NotNil(t, opts.Sandbox, "resolveProject should have built a sandbox")
	name, args := opts.Sandbox.Wrap("sh", []string{"-c", "true"})
	return name + " " + strings.Join(args, " ")
}

// TestProjectProfilesApplied is the regression guard for the bug where a
// caller-supplied sandbox (web defaults, scheduler jobs) caused the project's
// .acpp.yaml profiles to be silently dropped, so a session never got e.g. the
// docker socket even though `acpp sandbox bash` in the same directory did.
//
// The two subtests cover the two halves of the fix:
//   - project .acpp.yaml declares the profiles (the reported scenario), and
//   - the caller passes profiles as strings with no project sandbox block
//     (the scheduler scenario, and the line resolveProject previously hardcoded
//     to "").
//
// Both must end with the docker profile's socket bind present in the wrapped
// command, and the resolved profiles recorded back on opts.
func TestProjectProfilesApplied(t *testing.T) {
	t.Run("from project .acpp.yaml, caller only sets type", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "sandbox:\n  name: bbwrap\n  profiles: docker\n")

		// Mirrors what the web channel now passes: a default sandbox type, no
		// pre-built Sandbox, so the project file is folded in.
		opts := types.SessionOpts{CWD: dir, SandboxType: "bbwrap"}
		line := wrapArgs(t, &opts)

		require.Contains(t, line, "docker.sock",
			"project .acpp.yaml docker profile must reach the session sandbox")
		require.Equal(t, "docker", opts.SandboxProfiles)
	})

	t.Run("from caller profiles, no project sandbox block", func(t *testing.T) {
		dir := t.TempDir() // no .acpp.yaml

		// Mirrors a scheduled job with sandbox_profiles set. Before the fix the
		// caller's profiles were dropped here (resolveProject used a literal "").
		opts := types.SessionOpts{CWD: dir, SandboxType: "bbwrap", SandboxProfiles: "docker"}
		line := wrapArgs(t, &opts)

		require.Contains(t, line, "docker.sock",
			"caller-supplied profiles must reach the session sandbox")
		require.Equal(t, "docker", opts.SandboxProfiles)
	})

	t.Run("no profiles means no docker bind", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "sandbox:\n  name: bbwrap\n")

		opts := types.SessionOpts{CWD: dir, SandboxType: "bbwrap"}
		line := wrapArgs(t, &opts)

		require.NotContains(t, line, "docker.sock",
			"a session without the docker profile must not get the docker socket")
	})
}

func writeProjectFile(t *testing.T, dir, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".acpp.yaml"), []byte(body), 0o644))
}
