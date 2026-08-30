package integration

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/elek/acpp/db"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// TestSandboxDockerProfileE2E is the end-to-end proof that a project's stored
// sandbox profiles are actually applied to a live session: it starts a real
// conversation the way the web channel does (caller supplies only a sandbox type;
// Sandbox is left nil so Router.Create folds in the project row), then starts a
// real docker container through the resolved session sandbox — the same sandbox
// the `!` shell command runs in.
//
// The project config lives in a MemStore rather than postgres: resolveProject
// goes through the same db.ProjectStore interface either way, and an in-memory
// store keeps this test gated on docker/bwrap/rai alone.
//
// It is gated on docker, bwrap, rai and a reachable docker daemon; when any is
// missing it skips, so `go test ./...` stays green on machines without them.
// Note: it cannot pass from inside a profile-less acpp session (the very bug it
// guards) because there is no docker socket to bind — run it on the host, or via
// `acpp sandbox --profiles docker`.
func TestSandboxDockerProfileE2E(t *testing.T) {
	requireTool(t, "bwrap")
	requireTool(t, "docker")
	requireTool(t, "rai")
	requireDockerDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	proj := t.TempDir()
	// rai acp fake is the ACP agent used across the integration suite; the docker
	// profile is what must reach the session for the container to start.
	const project = "proj"
	store := db.NewMemStore()
	require.NoError(t, store.SetProjectField(ctx, project, "agent", "rai acp fake"))
	require.NoError(t, store.SetProjectField(ctx, project, "sandbox", "bbwrap"))
	require.NoError(t, store.SetProjectField(ctx, project, "sandbox_profiles", "docker"))

	r := router.New(router.WithProjects(store))
	t.Cleanup(r.Close)

	// Mirrors web.StartSessionWeb after the fix: a default sandbox type, no
	// pre-built Sandbox, so the project's profiles are folded in by the router.
	id, err := r.Create(ctx, types.SessionOpts{
		ProjectID:   project,
		CWD:         proj,
		Source:      "test",
		SandboxType: "bbwrap",
	})
	require.NoError(t, err)

	id, err = r.WaitReady(ctx, id)
	require.NoError(t, err, "agent should start inside the docker-profile sandbox")

	opts, ok := r.Opts(id.ConversationID)
	require.True(t, ok, "session opts should be available after WaitReady")
	require.NotNil(t, opts.Sandbox, "session should have a sandbox")
	require.Equal(t, "docker", opts.SandboxProfiles, "docker profile should be recorded on the session")

	// Start a container through the session's sandbox, exactly as runInSandbox
	// (the `!` command) would. This is the assertion that the profile is live:
	// without the docker bind this connection fails.
	name, args := opts.Sandbox.Wrap("docker", []string{"run", "--rm", "hello-world"})
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	require.NoErrorf(t, err, "docker run inside session sandbox failed:\n%s", out)
	require.Contains(t, string(out), "Hello from Docker",
		"container should have started and produced hello-world output")
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not found on PATH; skipping docker profile e2e", name)
	}
}

func requireDockerDaemon(t *testing.T) {
	t.Helper()
	out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skipf("docker daemon not reachable; skipping docker profile e2e (%v)", err)
	}
	// The sandbox binds /var/lib/docker and the socket path from the docker
	// profile; if the socket isn't at the expected path the bind would fail.
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skipf("/var/run/docker.sock not present; skipping docker profile e2e")
	}
}
