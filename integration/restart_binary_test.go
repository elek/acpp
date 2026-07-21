package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/elek/acpp/db"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// TestRestartBinaryCompletesStaleSessions is the faithful, cross-process version
// of TestRestartCompletesStaleSessions: it builds and runs the real `acpp serve`
// binary (no Discord token, so web UI only) against the test database, creates a
// live session over the HTTP API, terminates the process, then restarts it and
// checks the database.
//
// It runs two ways, because how the process dies decides who finalizes the
// session:
//
//   - kill9   — SIGKILL. The process cannot finalize anything, so the session is
//     left stuck ('running'/'pending'). Only the NEXT process's startup cleanup
//     can rescue it. We assert it is still stuck right after the kill, then
//     completed after the restart (and that the restart logged the cleanup).
//   - graceful — SIGTERM. The process shuts down cleanly and the router finalizes
//     the conversation on the way out — the session is completed DURING shutdown,
//     before any restart. We assert it is already complete after the signal.
//
// Either way, once a new process has come up, no session is left active.
func TestRestartBinaryCompletesStaleSessions(t *testing.T) {
	dsn := envOrSkip(t)
	ctx := context.Background()
	bin := buildACPP(t)

	cases := []struct {
		name     string
		sig      syscall.Signal
		graceful bool
	}{
		{"kill9", syscall.SIGKILL, false},
		{"graceful", syscall.SIGTERM, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanContent(t, ctx, dsn)

			store, err := db.Connect(ctx, dsn)
			require.NoError(t, err)
			t.Cleanup(store.Close)

			configHome := t.TempDir()
			proj := filepath.Join(t.TempDir(), "proj")
			require.NoError(t, os.MkdirAll(proj, 0o755))

			port := freePort(t)
			base := fmt.Sprintf("http://127.0.0.1:%d", port)
			writeWebConfig(t, configHome, dsn, port)

			// First boot, and a live session that never gets a deliberate close.
			first := startServe(t, bin, configHome, port)
			waitHealthy(t, base)
			sid := createWebSession(t, base, proj)
			waitSessionActive(t, ctx, store, sid)

			// Terminate the first process. Kill the leader only, the way systemd /
			// an OOM kill would; a cleanup reaps the whole group so no orphaned
			// agent lingers.
			require.NoError(t, syscall.Kill(first.Process.Pid, tc.sig))
			_ = first.Wait()
			_ = syscall.Kill(-first.Process.Pid, syscall.SIGKILL)

			if tc.graceful {
				// Clean shutdown finalizes the session on the way out.
				waitSessionStatus(t, ctx, store, sid, string(types.StatusComplete))
			} else {
				// A hard kill leaves the session un-finalized until a restart.
				row, err := store.GetSession(ctx, sid)
				require.NoError(t, err)
				require.Contains(t,
					[]string{string(types.StatusRunning), string(types.StatusPending)}, row.Status,
					"kill -9 must leave the session stuck until the next process cleans it up")
			}

			// Restart: the new process's startup cleanup must complete anything the
			// previous run left active.
			second := startServe(t, bin, configHome, port)
			t.Cleanup(func() {
				_ = syscall.Kill(-second.Process.Pid, syscall.SIGKILL)
				_ = second.Wait()
			})
			waitHealthy(t, base)

			// After the restart the session is finalized and stamped.
			waitSessionStatus(t, ctx, store, sid, string(types.StatusComplete))
			row, err := store.GetSession(ctx, sid)
			require.NoError(t, err)
			require.NotNil(t, row.FinishedAt, "completed session must have finished_at")

			if !tc.graceful {
				// The restart is the only thing that could have completed it, so it
				// must have logged the startup cleanup.
				require.Eventually(t, func() bool {
					log, _ := os.ReadFile(serverLogPath(configHome, port))
					return strings.Contains(string(log),
						"marked stale sessions from previous run as complete")
				}, 5*time.Second, 100*time.Millisecond,
					"restart should log that it cleaned up stale sessions")
			}
		})
	}
}

// buildACPP compiles the acpp binary from the repo root (one level up from the
// integration package) into a temp path and returns it.
func buildACPP(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "acpp")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building acpp: %s", out)
	return bin
}

// freePort returns a currently-free TCP port on the loopback interface.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// serverLogPath is where startServe sends a server instance's stdout/stderr.
func serverLogPath(configHome string, port int) string {
	return filepath.Join(configHome, fmt.Sprintf("server-%d.log", port))
}

// writeWebConfig writes the config.yaml an `acpp serve` instance reads from its
// private XDG_CONFIG_HOME: the test database, the listen port, and the offline
// fake agent with no sandbox.
func writeWebConfig(t *testing.T, configHome, dsn string, port int) {
	t.Helper()
	dir := filepath.Join(configHome, "acpp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := fmt.Sprintf("database:\n  dsn: %q\nweb_addr: \":%d\"\ndefaults:\n  agent: \"rai acp fake\"\n  sandbox: none\n",
		dsn, port)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644))
}

// startServe launches `acpp serve` with the given private config home in its own
// process group (so the whole tree can be killed) and returns the running
// command. Output is captured to serverLogPath for later inspection.
func startServe(t *testing.T, bin, configHome string, port int) *exec.Cmd {
	t.Helper()
	logFile, err := os.Create(serverLogPath(configHome, port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = logFile.Close() })

	cmd := exec.Command(bin, "serve", "--addr", fmt.Sprintf(":%d", port))
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configHome)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	return cmd
}

// waitHealthy blocks until the server answers GET /api/health.
func waitHealthy(t *testing.T, base string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get(base + "/api/health")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 30*time.Second, 200*time.Millisecond, "server never became healthy at %s", base)
}

// createWebSession creates a session via POST /session (the same path the web UI
// uses) and returns its id.
func createWebSession(t *testing.T, base, dir string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"dir": dir, "agent": "rai acp fake"})
	require.NoError(t, err)
	resp, err := http.Post(base+"/session", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "POST /session should succeed")
	var out struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(t, out.ID)
	return out.ID
}

// waitSessionActive blocks until the session row exists and is running/pending.
func waitSessionActive(t *testing.T, ctx context.Context, store db.Store, sid string) {
	t.Helper()
	require.Eventually(t, func() bool {
		row, err := store.GetSession(ctx, sid)
		if err != nil {
			return false
		}
		return row.Status == string(types.StatusRunning) || row.Status == string(types.StatusPending)
	}, 20*time.Second, 100*time.Millisecond, "session %s should become active", sid)
}

// waitSessionStatus blocks until the session reaches the wanted status.
func waitSessionStatus(t *testing.T, ctx context.Context, store db.Store, sid, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		row, err := store.GetSession(ctx, sid)
		return err == nil && row.Status == want
	}, 20*time.Second, 100*time.Millisecond, "session %s should reach status %q", sid, want)
}
