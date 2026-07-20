package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/elek/acpp/db"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// TestRestartCompletesStaleSessions exercises, at the store level and against a
// real PostgreSQL database, the stale-session cleanup every daemon runs on
// startup (cli/store.go: openStore calls store.CompleteRunningSessions, so both
// `acpp web` and `acpp serve` finalize stale sessions on boot).
//
// When the process restarts, any session left 'running' or 'pending' by the
// previous process must be marked 'complete' so stale sessions don't linger as
// active — while sessions that were already finalized are left alone. The two
// stuck states model the two ways a process can go down:
//
//   - running / pending — a hard crash or kill -9: nothing finalized the session.
//   - complete          — a clean shutdown, which finalizes its session on the
//     way out ("some sessions can be finished during shutdown").
//
// The states are constructed directly through the store so the test is
// deterministic (the full cross-process path — actually killing and restarting
// the binary — is covered by TestRestartBinaryCompletesStaleSessions).
// Simulating the restart is exactly the one line the web command runs on boot:
// CompleteRunningSessions must complete the two stuck sessions (and only those),
// stamping finished_at, and must not re-stamp the already-finished one.
func TestRestartCompletesStaleSessions(t *testing.T) {
	dsn := envOrSkip(t)
	ctx := context.Background()
	cleanContent(t, ctx, dsn)

	// A fresh store connection stands in for the restarted process: it opens the
	// same database the previous run left rows in.
	store, err := db.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(store.Close)

	insert := func(id, status string) {
		require.NoError(t, store.InsertSession(ctx, id, "test", "rai",
			filepath.Join(t.TempDir(), id), "none", "", "", id, nil, time.Now()))
		switch status {
		case string(types.StatusRunning):
			require.NoError(t, store.UpdateSession(ctx, id, types.StatusInfo{Status: types.StatusRunning}))
		case string(types.StatusComplete):
			require.NoError(t, store.FinishSession(ctx, id, types.StatusInfo{Status: types.StatusComplete}, ""))
		case string(types.StatusPending):
			// InsertSession already leaves the session pending.
		}
	}

	// Two stuck sessions, as a crash / kill -9 leaves them.
	insert("s-running", string(types.StatusRunning))
	insert("s-pending", string(types.StatusPending))
	// One already finalized, as a clean shutdown leaves it.
	insert("s-complete", string(types.StatusComplete))

	grRow, err := store.GetSession(ctx, "s-complete")
	require.NoError(t, err)
	require.NotNil(t, grRow.FinishedAt, "a finalized session must be stamped")
	grFinished := *grRow.FinishedAt

	// The restart's startup cleanup: complete only the two stuck sessions.
	n, err := store.CompleteRunningSessions(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, n, "only the running and pending sessions are stale")

	// Both stuck sessions are now complete with a finished_at.
	for _, id := range []string{"s-running", "s-pending"} {
		row, err := store.GetSession(ctx, id)
		require.NoError(t, err)
		require.Equal(t, string(types.StatusComplete), row.Status, "stale session should be completed")
		require.NotNil(t, row.FinishedAt, "completed session must have finished_at")
	}

	// The already-finished session is untouched — same status, same timestamp.
	grRow2, err := store.GetSession(ctx, "s-complete")
	require.NoError(t, err)
	require.Equal(t, string(types.StatusComplete), grRow2.Status)
	require.NotNil(t, grRow2.FinishedAt)
	require.WithinDuration(t, grFinished, *grRow2.FinishedAt, 0,
		"cleanup must not re-stamp an already-finished session")
}
