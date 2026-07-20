package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// dsnEnv mirrors integration.DSNEnv: the connection string for the test database.
const dsnEnv = "ACPP_TEST_POSTGRES"

// TestOpenStoreCompletesStaleSessions pins the invariant that opening the store
// on daemon startup finalizes sessions a previous process left active. Both
// daemons that own sessions — `acpp web` and `acpp serve` — go through
// openStore, so putting the cleanup there is what stops stale sessions from
// lingering as "running" (green in the web UI) after a restart, regardless of
// which command was restarted.
func TestOpenStoreCompletesStaleSessions(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(dsnEnv))
	if dsn == "" {
		t.Skipf("%s not set; skipping", dsnEnv)
	}
	ctx := context.Background()

	// A previous process left rows behind: seed them through a store connection
	// that stands in for that earlier run. Connect first (db.Connect runs the
	// migrations), then truncate for isolation from other tests' rows.
	seed, err := db.Connect(ctx, dsn)
	require.NoError(t, err)
	truncateContent(t, ctx, dsn)
	insert := func(id, status string) {
		require.NoError(t, seed.InsertSession(ctx, id, "test", "rai",
			filepath.Join(t.TempDir(), id), "none", "", "", id, nil, time.Now()))
		switch status {
		case string(types.StatusRunning):
			require.NoError(t, seed.UpdateSession(ctx, id, types.StatusInfo{Status: types.StatusRunning}))
		case string(types.StatusComplete):
			require.NoError(t, seed.FinishSession(ctx, id, types.StatusInfo{Status: types.StatusComplete}, ""))
		case string(types.StatusPending):
			// InsertSession already leaves the session pending.
		}
	}
	insert("s-running", string(types.StatusRunning))
	insert("s-pending", string(types.StatusPending))
	insert("s-complete", string(types.StatusComplete))
	seed.Close()

	// The restarted daemon opens the same database.
	store, err := openStore(ctx, &config.Config{Database: config.DatabaseConfig{DSN: dsn}})
	require.NoError(t, err)
	t.Cleanup(store.Close)

	// The two stuck sessions are finalized; the already-complete one is untouched.
	for _, id := range []string{"s-running", "s-pending", "s-complete"} {
		row, err := store.GetSession(ctx, id)
		require.NoError(t, err)
		require.Equal(t, string(types.StatusComplete), row.Status,
			"session %s should be complete after startup", id)
	}
}

// truncateContent empties the session and project tables so the test observes
// only its own rows (CompleteRunningSessions completes every stale row).
func truncateContent(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	_, err = pool.Exec(ctx, `TRUNCATE TABLE session, project RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
}
