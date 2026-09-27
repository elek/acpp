package router

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/elek/acpp/process"
	"github.com/elek/acpp/types"
	"github.com/stretchr/testify/require"
)

// fakeHost records what the router asks of a remote host. Its handles never
// answer, which is enough to inspect Create and Close.
type fakeHost struct {
	name string

	mu      sync.Mutex
	specs   []process.Spec
	handles []*fakeHandle
}

func (h *fakeHost) Name() string { return h.name }

func (h *fakeHost) Start(_ context.Context, spec process.Spec) (process.Handle, error) {
	pr, pw := io.Pipe()
	hd := &fakeHandle{stdout: pr, stdoutW: pw, done: make(chan struct{})}
	h.mu.Lock()
	h.specs = append(h.specs, spec)
	h.handles = append(h.handles, hd)
	h.mu.Unlock()
	return hd, nil
}

func (h *fakeHost) Exec(context.Context, process.ExecSpec) (process.ExecResult, error) {
	return process.ExecResult{ExitCode: 1}, nil
}

type fakeHandle struct {
	stdout  *io.PipeReader
	stdoutW *io.PipeWriter
	done    chan struct{}

	mu       sync.Mutex
	closed   bool
	detached bool
	descs    [][]byte
}

func (f *fakeHandle) Stdio() (io.WriteCloser, io.Reader) { return nopWriteCloser{}, f.stdout }
func (f *fakeHandle) PID() int                           { return 4242 }
func (f *fakeHandle) Done() <-chan struct{}              { return f.done }
func (f *fakeHandle) Stderr() string                     { return "" }
func (f *fakeHandle) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.stdoutW.Close()
}
func (f *fakeHandle) UpdateDescriptor(d []byte) error {
	f.mu.Lock()
	f.descs = append(f.descs, d)
	f.mu.Unlock()
	return nil
}
func (f *fakeHandle) Detach() {
	f.mu.Lock()
	f.detached = true
	f.mu.Unlock()
	f.stdoutW.Close()
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

func remoteRouter(t *testing.T, fields map[string]string) (*Router, *fakeHost) {
	t.Helper()
	store := db.NewMemStore()
	for k, v := range fields {
		require.NoError(t, store.SetProjectField(context.Background(), "p", k, v))
	}
	host := &fakeHost{name: "box"}
	rt := New(
		WithConfig(&config.Config{AgentPath: []string{t.TempDir()}}),
		WithProjects(store),
		WithHosts(func(loc string) (process.Host, error) {
			require.Equal(t, "box", loc)
			return host, nil
		}),
	)
	return rt, host
}

// A remote project's agent starts on its host with the sandbox left for the
// host to build and the agent command left for the host to resolve.
func TestCreateOnRemoteLocation(t *testing.T) {
	rt, host := remoteRouter(t, map[string]string{
		"location":         "box",
		"agent":            "claude-code-acp",
		"sandbox":          "bbwrap",
		"sandbox_profiles": "docker",
		"sandbox_env":      "FOO",
	})
	meta, err := rt.Create(context.Background(), types.SessionOpts{ProjectID: "p", CWD: "/remote/repo"})
	require.NoError(t, err)
	require.Equal(t, 4242, meta.ProcessPID)

	require.Len(t, host.specs, 1)
	spec := host.specs[0]
	require.Equal(t, "claude-code-acp", spec.Agent, "the agent is resolved on the remote host")
	require.Nil(t, spec.Sandbox, "no local sandbox for a remote host")
	require.Equal(t, process.SandboxSpec{Type: "bbwrap", Profiles: "docker", PassEnv: []string{"FOO"}}, spec.SandboxSpec)
	require.Equal(t, meta.ConversationID, spec.ConversationID)
	require.Contains(t, string(spec.Descriptor), meta.ConversationID)

	opts, ok := rt.Opts(meta.ConversationID)
	require.True(t, ok)
	require.Equal(t, "box", opts.Location)
	require.Equal(t, "bbwrap", opts.SandboxType)
}

func TestCreateOnRemoteLocationWithoutHosts(t *testing.T) {
	store := db.NewMemStore()
	require.NoError(t, store.SetProjectField(context.Background(), "p", "location", "box"))
	rt := New(WithProjects(store))
	defer rt.Close()
	_, err := rt.Create(context.Background(), types.SessionOpts{ProjectID: "p", CWD: t.TempDir()})
	require.ErrorContains(t, err, "remote agents are not enabled")
}

// Shutting the server down detaches remote conversations: they are neither
// stopped nor finalized, so a restarted server can adopt them.
func TestCloseDetachesRemoteConversations(t *testing.T) {
	rt, host := remoteRouter(t, map[string]string{"location": "box"})
	var closed []types.ConversationClosed
	var mu sync.Mutex
	rt.Subscribe(func(_ context.Context, _ *json.RawMessage, _ types.ConversationMeta, msg any) {
		if c, ok := msg.(types.ConversationClosed); ok {
			mu.Lock()
			closed = append(closed, c)
			mu.Unlock()
		}
	})
	_, err := rt.Create(context.Background(), types.SessionOpts{ProjectID: "p", CWD: "/remote/repo"})
	require.NoError(t, err)

	rt.Close()
	hd := host.handles[0]
	hd.mu.Lock()
	defer hd.mu.Unlock()
	require.True(t, hd.detached)
	require.False(t, hd.closed)
	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, closed)
}

// A deliberate close stops the remote process like a local one.
func TestCloseConversationStopsRemoteProcess(t *testing.T) {
	rt, host := remoteRouter(t, map[string]string{"location": "box"})
	defer rt.Close()
	meta, err := rt.Create(context.Background(), types.SessionOpts{ProjectID: "p", CWD: "/remote/repo"})
	require.NoError(t, err)
	rt.CloseConversation(meta)
	hd := host.handles[0]
	hd.mu.Lock()
	defer hd.mu.Unlock()
	require.True(t, hd.closed)
}

// The same directory on two machines is not contended.
func TestRunningSessionsForDirIsPerLocation(t *testing.T) {
	rt, _ := remoteRouter(t, map[string]string{"location": "box"})
	defer rt.Close()
	_, err := rt.Create(context.Background(), types.SessionOpts{ProjectID: "p", CWD: "/repo"})
	require.NoError(t, err)
	require.Equal(t, 1, rt.runningSessionsForDir("box", "/repo"))
	require.Equal(t, 0, rt.runningSessionsForDir(process.LocalName, "/repo"))
}

// Adopt refuses descriptors it cannot resume from.
func TestAdoptRejectsIncompleteDescriptor(t *testing.T) {
	rt, host := remoteRouter(t, nil)
	defer rt.Close()
	hd, _ := host.Start(context.Background(), process.Spec{})
	_, err := rt.Adopt(context.Background(), host, hd, []byte(`{"conversation_id":"c1"}`))
	require.ErrorContains(t, err, "never finished its handshake")
	_, err = rt.Adopt(context.Background(), host, hd, []byte(`not json`))
	require.ErrorContains(t, err, "bad descriptor")
}
