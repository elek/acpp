package remote_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"net/http"
	"net/http/httptest"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/process"
	"github.com/elek/acpp/remote"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// buildStubAgent compiles the TCK's stub ACP agent.
func buildStubAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "stubagent")
	out, err := exec.Command("go", "build", "-o", bin, "../tck/testdata/stubagent").CombinedOutput()
	require.NoError(t, err, "building stub agent: %s", out)
	return bin
}

// recorder collects what a router fans out: agent text and turn ends per
// conversation, and adoptions.
type recorder struct {
	mu      sync.Mutex
	text    map[string]string
	turns   chan string
	adopted chan types.ConversationMeta
}

func newRecorder(rt *router.Router) *recorder {
	r := &recorder{text: map[string]string{}, turns: make(chan string, 16), adopted: make(chan types.ConversationMeta, 4)}
	rt.Subscribe(func(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
		switch m := msg.(type) {
		case acp.SessionNotification:
			if c := m.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
				r.mu.Lock()
				r.text[id.ConversationID] += c.Content.Text.Text
				r.mu.Unlock()
			}
		case acp.PromptResponse:
			r.turns <- id.ConversationID
		case types.ConversationAdopted:
			r.adopted <- m.Meta
		}
	})
	return r
}

func (r *recorder) prompt(t *testing.T, rt *router.Router, meta types.ConversationMeta, text string) string {
	t.Helper()
	r.mu.Lock()
	r.text[meta.ConversationID] = ""
	r.mu.Unlock()
	require.NoError(t, rt.Send(context.Background(), meta, acp.PromptRequest{
		SessionId: meta.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
	}))
	select {
	case id := <-r.turns:
		require.Equal(t, meta.ConversationID, id)
	case <-time.After(20 * time.Second):
		t.Fatal("turn did not finish")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text[meta.ConversationID]
}

// A conversation of a project located on a remote agent runs there end to end —
// prompts, shell commands — and survives a restart of the server: the new
// router adopts it and it keeps answering.
func TestRemoteConversationSurvivesServerRestart(t *testing.T) {
	stub := buildStubAgent(t)
	store := db.NewMemStore()
	ctx := context.Background()
	require.NoError(t, store.SetProjectField(ctx, "p", "location", "box"))
	require.NoError(t, store.SetProjectField(ctx, "p", "agent", stub))

	var current atomic.Pointer[remote.Hub]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().ServeHTTP(w, r)
	}))
	defer srv.Close()

	newServer := func() (*remote.Hub, *router.Router, *recorder) {
		hub := remote.NewHub("s")
		rt := router.New(router.WithProjects(store), router.WithHosts(hub.Host))
		rec := newRecorder(rt)
		hub.SetAdopt(func(ctx context.Context, host process.Host, h process.Handle, desc []byte) error {
			_, err := rt.Adopt(ctx, host, h, desc)
			return err
		})
		hub.SetFinalize(rt.Finalize)
		current.Store(hub)
		return hub, rt, rec
	}
	hub1, rt1, rec1 := newServer()

	// Without the agent connected the project cannot start.
	_, err := rt1.Create(ctx, types.SessionOpts{ProjectID: "p", CWD: t.TempDir()})
	require.ErrorContains(t, err, `location "box" is not connected`)

	agentCtx, stopAgent := context.WithCancel(ctx)
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		_ = (&remote.Agent{Server: srv.URL, Secret: "s", Location: "box"}).Run(agentCtx)
	}()
	defer func() {
		stopAgent()
		<-agentDone
	}()
	require.Eventually(t, func() bool { return hub1.Connected("box") }, 15*time.Second, 10*time.Millisecond)

	dir := t.TempDir()
	meta, err := rt1.Create(ctx, types.SessionOpts{ProjectID: "p", CWD: dir})
	require.NoError(t, err)
	meta, err = rt1.WaitReady(ctx, meta)
	require.NoError(t, err)
	require.Equal(t, "Madrid", rec1.prompt(t, rt1, meta, "What is the capital of Spain?"))

	// Shell commands run on the remote machine, in the session's directory.
	handled, out, err := rt1.HandleCommands(ctx, meta, "!pwd")
	require.NoError(t, err)
	require.True(t, handled)
	require.Contains(t, out, dir)

	// Restart the server: the router detaches, the hub drops the connection.
	rt1.Close()
	hub1.Close()
	hub2, rt2, rec2 := newServer()
	defer hub2.Close()
	defer rt2.Close()

	var adopted types.ConversationMeta
	select {
	case adopted = <-rec2.adopted:
	case <-time.After(20 * time.Second):
		t.Fatal("the new server did not adopt the conversation")
	}
	require.Equal(t, meta.ConversationID, adopted.ConversationID)
	require.Equal(t, meta.SessionID, adopted.SessionID)
	require.True(t, rt2.Active(meta.ConversationID))

	answer := rec2.prompt(t, rt2, adopted, "What is the capital of Spain, again?")
	require.Equal(t, "Madrid", answer)
	// The agent kept its state across the restart: it is the same process.
	require.Equal(t, meta.ProcessPID, adopted.ProcessPID)

	rt2.CloseConversation(adopted)
	require.False(t, rt2.Active(meta.ConversationID))
	require.False(t, strings.Contains(rec2.text[meta.ConversationID], "error"))
}
