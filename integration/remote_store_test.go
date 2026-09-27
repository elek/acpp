package integration

import (
	"context"
	"testing"
	"time"

	"github.com/elek/acpp/db"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// The store keeps a project's location, lists it, and can bring a session that
// startup marked complete back to life for a conversation adopted from a remote
// host.
func TestProjectLocationAndReopenSession(t *testing.T) {
	WithRouter(t, func(t *testing.T, dir string, _ *router.Router, store db.Store) {
		ctx := context.Background()
		require.NoError(t, store.SetProjectField(ctx, "p", "location", "gpu-box"))
		p, err := store.GetProject(ctx, "p")
		require.NoError(t, err)
		require.Equal(t, "gpu-box", p.Location)

		list, err := store.ListProjects(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, "gpu-box", list[0].Location)

		require.NoError(t, store.InsertSession(ctx, "11111111-1111-1111-1111-111111111111", "web", "agent", dir, "", "gpu-box", "", "p", nil, time.Now()))
		require.NoError(t, store.UpdateSession(ctx, "11111111-1111-1111-1111-111111111111", types.StatusInfo{Status: types.StatusRunning}))
		n, err := store.CompleteRunningSessions(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		require.NoError(t, store.ReopenSession(ctx, "11111111-1111-1111-1111-111111111111"))
		row, err := store.GetSession(ctx, "11111111-1111-1111-1111-111111111111")
		require.NoError(t, err)
		require.Equal(t, "pending", row.Status)
		require.Nil(t, row.FinishedAt)
		require.Equal(t, "gpu-box", row.Node)
	})
}
