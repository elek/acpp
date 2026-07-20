package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// TestSubprocessExitFinalizesConversation exercises the WIP subprocess-exit
// finalization end to end against a real router, a real agent subprocess
// (rai acp fake) and the real PostgreSQL persistence subscriber.
//
// It runs a genuine conversation (create -> ready -> prompt -> turn completes),
// then kills the agent process the way a crash or OOM-kill would — leaving the
// conversation with no deliberate close. The router's subprocess-exit watcher
// must notice the process is gone and finalize the conversation: fan an errored
// ConversationClosed, drop the conversation so it is no longer Active, and (via
// the persister) record the session as errored with a finished_at timestamp.
//
// Without the patch the conversation would stay registered forever (Active
// true, session row stuck 'pending'), which is exactly what wedges a scheduled
// job as "previous run still active".
func TestSubprocessExitFinalizesConversation(t *testing.T) {
	WithRouter(t, func(t *testing.T, dir string, r *router.Router) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// Observe the router's event stream so we can watch the conversation first
		// complete a real turn and then be finalized when its subprocess dies.
		responded := make(chan struct{}, 1)
		closed := make(chan types.ConversationClosed, 1)
		r.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
			switch m := msg.(type) {
			case acp.PromptResponse:
				select {
				case responded <- struct{}{}:
				default:
				}
			case types.ConversationClosed:
				select {
				case closed <- m:
				default:
				}
			}
		})

		// A project backed by the real rai acp fake agent subprocess.
		proj := filepath.Join(dir, "proj")
		require.NoError(t, os.MkdirAll(proj, 0o755))

		id, err := r.Create(ctx, types.SessionOpts{
			ProjectID: proj,
			Agent:     "rai acp fake",
			CWD:       proj,
			Source:    "test",
		})
		require.NoError(t, err)
		require.NotZero(t, id.ProcessPID, "Create should report a live subprocess PID")

		id, err = r.WaitReady(ctx, id)
		require.NoError(t, err)
		require.NotEmpty(t, id.SessionID)
		require.True(t, r.Active(id.ConversationID), "conversation should be active once ready")

		// Simulate a conversation: prompt the agent and wait for it to finish the turn.
		err = r.Send(ctx, id, acp.PromptRequest{
			SessionId: id.SessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello there, please do some work")},
		})
		require.NoError(t, err)

		select {
		case <-responded:
		case <-ctx.Done():
			t.Fatal("agent never completed the prompt turn")
		}

		// rai acp fake is a server: it stays alive after a turn. Kill the whole
		// process group hard, as if it had crashed, so nothing closes the
		// conversation deliberately — the router's subprocess-exit watcher is the
		// only thing that can finalize it.
		require.NoError(t, syscall.Kill(-id.ProcessPID, syscall.SIGKILL))

		// The router fans an errored ConversationClosed.
		select {
		case c := <-closed:
			require.Equal(t, id.ConversationID, c.Meta.ConversationID)
			require.NotEmpty(t, c.Err, "a subprocess-exit close must be marked as an error")
		case <-ctx.Done():
			t.Fatal("router did not finalize the conversation after the subprocess was killed")
		}

		// ...and drops the conversation so it is no longer active.
		require.Eventually(t, func() bool {
			return !r.Active(id.ConversationID)
		}, 10*time.Second, 100*time.Millisecond,
			"conversation should be dropped once its subprocess exits")

		// The persistence subscriber records the session as errored and stamped,
		// not left stuck 'pending'. Verify against the same test database.
		store, err := db.Connect(ctx, envOrSkip(t))
		require.NoError(t, err)
		defer store.Close()

		require.Eventually(t, func() bool {
			row, err := store.GetSession(ctx, string(id.SessionID))
			if err != nil {
				return false
			}
			return row.Status == string(types.StatusError) &&
				row.ErrorMsg != "" && row.FinishedAt != nil
		}, 10*time.Second, 100*time.Millisecond,
			"session should be persisted as errored with a finished_at timestamp")
	})
}
