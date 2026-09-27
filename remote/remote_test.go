package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elek/acpp/process"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

const testSecret = "s3cret"

// server is an httptest server whose hub can be swapped, standing in for a
// server process that restarts at the same address. A nil hub refuses agents.
type server struct {
	*httptest.Server
	hub atomic.Pointer[Hub]
}

func newServer(t *testing.T, hub *Hub) *server {
	t.Helper()
	s := &server{}
	s.hub.Store(hub)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := s.hub.Load()
		if h == nil {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// startAgent runs an Agent against srv until the test ends.
func startAgent(t *testing.T, srv *server, mod func(*Agent)) *Agent {
	t.Helper()
	a := &Agent{Server: srv.URL, Secret: testSecret, Location: "box"}
	if mod != nil {
		mod(a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("agent did not stop")
		}
	})
	return a
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func connectedHost(t *testing.T, hub *Hub) process.Host {
	t.Helper()
	waitFor(t, "agent to connect", func() bool { return hub.Connected("box") })
	h, err := hub.Host("box")
	require.NoError(t, err)
	return h
}

func start(t *testing.T, h process.Host, agent string) process.Handle {
	t.Helper()
	ps, err := h.Start(context.Background(), process.Spec{Agent: agent, ConversationID: uuid.NewString()})
	require.NoError(t, err)
	return ps
}

// dropConnection kills the hub's current connection for the location, as a
// network failure would.
func dropConnection(t *testing.T, hub *Hub) {
	t.Helper()
	hub.mu.Lock()
	c := hub.locs["box"].conn
	hub.mu.Unlock()
	require.NotNil(t, c)
	c.close()
	waitFor(t, "hub to notice the drop", func() bool { return !hub.Connected("box") })
}

func TestSpawnEcho(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub)

	ps := start(t, h, "cat")
	require.NotZero(t, ps.PID())
	stdin, stdout := ps.Stdio()
	_, err := io.WriteString(stdin, "hello\n")
	require.NoError(t, err)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "hello\n", line)

	ps.Close()
	select {
	case <-ps.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("process not done after Close")
	}
}

func TestSpawnFailure(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub)

	_, err := h.Start(context.Background(), process.Spec{Agent: "/nonexistent/agent", ConversationID: uuid.NewString()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "location \"box\"")
}

func TestExitIsReported(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub)

	ps := start(t, h, `sh -c "echo bye; echo oops >&2; exit 1"`)
	_, stdout := ps.Stdio()
	out, err := io.ReadAll(stdout)
	require.NoError(t, err)
	require.Equal(t, "bye\n", string(out))
	select {
	case <-ps.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("process not done after exiting")
	}
	require.Contains(t, ps.Stderr(), "oops")
}

func TestExec(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub)

	res, err := h.Exec(context.Background(), process.ExecSpec{
		Argv: []string{"sh", "-c", "echo out; echo err >&2; exit 3"},
		Dir:  t.TempDir(),
	})
	require.NoError(t, err)
	require.Equal(t, 3, res.ExitCode)
	require.Equal(t, "out\n", string(res.Stdout))
	require.Equal(t, "err\n", string(res.Stderr))

	res, err = h.Exec(context.Background(), process.ExecSpec{
		Argv:     []string{"sh", "-c", "echo a; echo b >&2"},
		Combined: true,
	})
	require.NoError(t, err)
	require.Equal(t, "a\nb\n", string(res.Stdout))

	// A command that cannot start is an error, not an exit code.
	_, err = h.Exec(context.Background(), process.ExecSpec{Argv: []string{"/nonexistent/cmd"}})
	require.Error(t, err)
}

func TestBadSecretIsFatal(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	a := &Agent{Server: srv.URL, Secret: "wrong", Location: "box"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := a.Run(ctx)
	require.ErrorIs(t, err, ErrFatal)
	require.Contains(t, err.Error(), "401")
}

func TestDuplicateLocationRefused(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	connectedHost(t, hub)

	url, err := websocketURL(srv.URL)
	require.NoError(t, err)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+testSecret)
	hdr.Set(headerLocation, "box")
	hdr.Set(headerProtocol, ProtocolVersion)
	_, resp, err := websocket.DefaultDialer.Dial(url, hdr)
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestNotConnected(t *testing.T) {
	hub := NewHub(testSecret)
	_, err := hub.Host("nowhere")
	require.ErrorContains(t, err, `location "nowhere" is not connected`)
}

// A dropped connection loses nothing in either direction: output produced
// while disconnected is replayed from the remote agent's buffer, input written
// while disconnected from the hub's, each exactly once and in order.
func TestReconnectReplaysBothDirections(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub)

	// Echo stdin, and also emit a numbered line every 50ms.
	// (sh gives a background job /dev/null as stdin, hence the fd 3 dance.)
	ps := start(t, h, `sh -c "exec 3<&0; cat <&3 & i=0; while [ \$i -lt 40 ]; do echo tick\$i; i=\$((i+1)); sleep 0.05; done; wait"`)
	stdin, stdout := ps.Stdio()
	r := bufio.NewReader(stdout)

	readLine := func() string {
		line, err := r.ReadString('\n')
		require.NoError(t, err)
		return strings.TrimSpace(line)
	}
	require.Equal(t, "tick0", readLine())

	dropConnection(t, hub)
	// Written while disconnected: buffered by the hub, sent after the reconnect.
	_, err := io.WriteString(stdin, "echoed\n")
	require.NoError(t, err)

	var ticks []string
	sawEcho := false
	for len(ticks) < 39 {
		line := readLine()
		if line == "echoed" {
			sawEcho = true
			continue
		}
		ticks = append(ticks, line)
	}
	for i, tick := range ticks {
		require.Equal(t, fmt.Sprintf("tick%d", i+1), tick)
	}
	if !sawEcho {
		require.Equal(t, "echoed", readLine())
	}
	ps.Close()
}

// When the remote agent stays away past the grace period, the hub gives up on
// its processes and the agent stops them.
func TestGraceExpiry(t *testing.T) {
	hub := NewHub(testSecret, WithGrace(300*time.Millisecond))
	srv := newServer(t, hub)
	a := startAgent(t, srv, func(a *Agent) { a.Grace = 300 * time.Millisecond })
	h := connectedHost(t, hub)
	ps := start(t, h, "cat")

	srv.hub.Store(nil) // refuse the reconnect
	dropConnection(t, hub)

	select {
	case <-ps.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("hub did not give up on the process")
	}
	require.Contains(t, ps.Stderr(), "disconnected for more than")

	waitFor(t, "agent to stop the process", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, s := range a.streams {
			s.mu.Lock()
			exited := s.exited
			s.mu.Unlock()
			if !exited {
				return false
			}
		}
		return true
	})
}

// A process outlives a server restart: the new server adopts it from the
// descriptor the old one left with the remote agent, and it keeps working.
func TestServerRestartAdopts(t *testing.T) {
	hub1 := NewHub(testSecret)
	srv := newServer(t, hub1)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub1)
	ps := start(t, h, "cat")
	stdin, stdout := ps.Stdio()
	_, err := io.WriteString(stdin, "before\n")
	require.NoError(t, err)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "before\n", line)

	require.NoError(t, ps.(process.Detachable).UpdateDescriptor([]byte(`{"v":2}`)))
	// The descriptor message is asynchronous; wait for the agent to hold it.
	time.Sleep(200 * time.Millisecond)
	ps.(process.Detachable).Detach()
	hub1.Close()

	type adopted struct {
		h    process.Handle
		desc string
		host string
	}
	got := make(chan adopted, 1)
	hub2 := NewHub(testSecret, WithAdopt(func(ctx context.Context, host process.Host, h process.Handle, desc []byte) error {
		got <- adopted{h, string(desc), host.Name()}
		return nil
	}))
	srv.hub.Store(hub2)

	var a adopted
	select {
	case a = <-got:
	case <-time.After(15 * time.Second):
		t.Fatal("new hub did not adopt the process")
	}
	require.Equal(t, `{"v":2}`, a.desc)
	require.Equal(t, "box", a.host)
	require.Equal(t, ps.PID(), a.h.PID())

	stdin, stdout = a.h.Stdio()
	_, err = io.WriteString(stdin, "after\n")
	require.NoError(t, err)
	line, err = bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "after\n", line)
	a.h.Close()
}

// A new server that cannot adopt a process makes the agent stop it.
func TestServerRestartRefusedAdoptStopsProcess(t *testing.T) {
	hub1 := NewHub(testSecret)
	srv := newServer(t, hub1)
	agent := startAgent(t, srv, nil)
	h := connectedHost(t, hub1)
	ps := start(t, h, "cat")
	ps.(process.Detachable).Detach()
	hub1.Close()

	hub2 := NewHub(testSecret, WithAdopt(func(context.Context, process.Host, process.Handle, []byte) error {
		return errors.New("no thanks")
	}))
	srv.hub.Store(hub2)
	connectedHost(t, hub2)
	waitFor(t, "agent to drop the stream", func() bool {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return len(agent.streams) == 0
	})
}

// A process that expired while no server was connected is reported to the next
// server, which finalizes its conversation.
func TestExpiredWhileAwayIsFinalized(t *testing.T) {
	hub1 := NewHub(testSecret)
	srv := newServer(t, hub1)
	startAgent(t, srv, func(a *Agent) { a.Grace = 200 * time.Millisecond })
	h := connectedHost(t, hub1)
	ps := start(t, h, "cat")
	require.NoError(t, ps.(process.Detachable).UpdateDescriptor([]byte(`{"id":"x"}`)))
	time.Sleep(200 * time.Millisecond)
	ps.(process.Detachable).Detach()
	srv.hub.Store(nil)
	hub1.Close()

	time.Sleep(time.Second) // past the agent's grace period

	var mu sync.Mutex
	var finalized []string
	hub2 := NewHub(testSecret, WithFinalize(func(desc []byte, errMsg string) {
		mu.Lock()
		finalized = append(finalized, string(desc)+" "+errMsg)
		mu.Unlock()
	}))
	srv.hub.Store(hub2)
	waitFor(t, "finalize", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(finalized) == 1
	})
	require.Contains(t, finalized[0], `{"id":"x"}`)
	require.Contains(t, finalized[0], "no server connected")
}

func TestCloseWhileDisconnectedStopsOnReturn(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	agent := startAgent(t, srv, nil)
	h := connectedHost(t, hub)
	ps := start(t, h, "cat")

	dropConnection(t, hub)
	ps.Close() // returns at once: nobody to ask
	select {
	case <-ps.Done():
	default:
		t.Fatal("Close while disconnected must finish the handle")
	}
	connectedHost(t, hub)
	waitFor(t, "agent to drop the stream", func() bool {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return len(agent.streams) == 0
	})
}

func TestExecWhileDisconnectedFails(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub)
	srv.hub.Store(nil)
	dropConnection(t, hub)

	_, err := h.Exec(context.Background(), process.ExecSpec{Argv: []string{"true"}})
	require.ErrorContains(t, err, "not connected")
}

func TestWebsocketURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://h:8080":       "ws://h:8080/remote/ws",
		"https://h/":          "wss://h/remote/ws",
		"https://h/prefix":    "wss://h/prefix/remote/ws",
		"ws://h:1/remote/ws/": "ws://h:1/remote/ws/remote/ws",
	} {
		got, err := websocketURL(in)
		require.NoError(t, err)
		require.Equal(t, want, got, in)
	}
	_, err := websocketURL("ftp://h")
	require.Error(t, err)
}

// Stopping the remote agent stops its processes and tells the server at once,
// instead of leaving the server to wait out its grace period.
func TestAgentShutdownReportsExits(t *testing.T) {
	hub := NewHub(testSecret) // default 5 minute grace
	srv := newServer(t, hub)
	a := &Agent{Server: srv.URL, Secret: testSecret, Location: "box"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	h := connectedHost(t, hub)
	ps := start(t, h, "cat")

	cancel()
	select {
	case <-ps.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("server did not hear the exit")
	}
	require.NoError(t, <-done)
}

// rawAgent speaks the protocol by hand, so a test can script exactly what a
// remote agent reports.
type rawAgent struct {
	t  *testing.T
	ws *websocket.Conn
}

func dialRaw(t *testing.T, srv *server, hello *message) (*rawAgent, *message) {
	t.Helper()
	url, err := websocketURL(srv.URL)
	require.NoError(t, err)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+testSecret)
	hdr.Set(headerLocation, "box")
	hdr.Set(headerProtocol, ProtocolVersion)
	ws, _, err := websocket.DefaultDialer.Dial(url, hdr)
	require.NoError(t, err)
	t.Cleanup(func() { ws.Close() })
	require.NoError(t, ws.WriteJSON(hello))
	var welcome message
	require.NoError(t, ws.ReadJSON(&welcome))
	require.Equal(t, msgWelcome, welcome.Type)
	return &rawAgent{t: t, ws: ws}, &welcome
}

// A stream the hub already finalized, reported again as exited (its forget was
// lost), is stopped without finalizing the conversation a second time; one the
// hub never saw is finalized.
func TestExitedStreamFinalizedOnce(t *testing.T) {
	var finalized []string
	var mu sync.Mutex
	hub := NewHub(testSecret, WithFinalize(func(desc []byte, _ string) {
		mu.Lock()
		finalized = append(finalized, string(desc))
		mu.Unlock()
	}))
	srv := newServer(t, hub)

	seen, unseen := uuid.New(), uuid.New()
	hub.mu.Lock()
	hub.loc("box").finished[seen] = time.Now()
	hub.mu.Unlock()

	_, welcome := dialRaw(t, srv, &message{Type: msgHello, Streams: []streamInfo{
		{ID: seen.String(), Exited: true, Descriptor: []byte(`"seen"`)},
		{ID: unseen.String(), Exited: true, Descriptor: []byte(`"unseen"`)},
	}})
	require.Len(t, welcome.Resume, 2)
	for _, r := range welcome.Resume {
		require.Equal(t, actionKill, r.Action)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{`"unseen"`}, finalized)
}

// When the agent's stdin ends mid-line (a message the old server was cut off
// sending), the adopting server terminates that line before sending its own.
func TestAdoptTerminatesCutOffStdinLine(t *testing.T) {
	adopted := make(chan process.Handle, 1)
	hub := NewHub(testSecret, WithAdopt(func(_ context.Context, _ process.Host, h process.Handle, _ []byte) error {
		adopted <- h
		return nil
	}))
	srv := newServer(t, hub)
	id := uuid.New()
	raw, welcome := dialRaw(t, srv, &message{Type: msgHello, Streams: []streamInfo{
		{ID: id.String(), PID: 7, OutAcked: 100, InReceived: 50, InLineEnd: false},
	}})
	require.Equal(t, []resumeInfo{{ID: id.String(), Action: actionResume, OutFrom: 100}}, welcome.Resume)
	h := <-adopted
	require.Equal(t, 7, h.PID())

	stdin, _ := h.Stdio()
	_, err := io.WriteString(stdin, `{"cancel":1}`+"\n")
	require.NoError(t, err)

	var got []byte
	for len(got) < len("\n"+`{"cancel":1}`+"\n") {
		typ, b, err := raw.ws.ReadMessage()
		require.NoError(t, err)
		if typ != websocket.BinaryMessage {
			continue
		}
		sid, kind, off, p, err := decodeData(b)
		require.NoError(t, err)
		require.Equal(t, id, sid)
		require.Equal(t, kindStdin, kind)
		require.Equal(t, uint64(50+len(got)), off)
		got = append(got, p...)
	}
	require.Equal(t, "\n"+`{"cancel":1}`+"\n", string(got))
}

// After the hub gave up on a location, an agent that comes back late (its own
// timer lagged) is told to stop those processes rather than having them
// adopted.
func TestLateReturnAfterHubExpiryStopsProcesses(t *testing.T) {
	adopts := 0
	hub := NewHub(testSecret, WithGrace(200*time.Millisecond), WithAdopt(func(context.Context, process.Host, process.Handle, []byte) error {
		adopts++
		return nil
	}))
	srv := newServer(t, hub)
	agent := startAgent(t, srv, func(a *Agent) { a.Grace = time.Hour })
	h := connectedHost(t, hub)
	ps := start(t, h, "cat")

	srv.hub.Store(nil)
	dropConnection(t, hub)
	<-ps.Done() // the hub expired it
	srv.hub.Store(hub)

	connectedHost(t, hub)
	waitFor(t, "agent to drop the stream", func() bool {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return len(agent.streams) == 0
	})
	require.Zero(t, adopts)
}

func TestExecHonorsDeadline(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.Exec(ctx, process.ExecSpec{Argv: []string{"sleep", "30"}})
	require.Error(t, err)

	require.Less(t, time.Since(start), 10*time.Second)

	// The remote command was bounded too: nothing is left sleeping.
	waitFor(t, "remote sleep to be killed", func() bool {
		res, err := h.Exec(context.Background(), process.ExecSpec{Argv: []string{"sh", "-c", "pgrep -x -f 'sleep 30' || true"}})
		return err == nil && len(strings.TrimSpace(string(res.Stdout))) == 0
	})
}

func TestExecOutputTruncated(t *testing.T) {
	hub := NewHub(testSecret)
	srv := newServer(t, hub)
	startAgent(t, srv, nil)
	h := connectedHost(t, hub)
	res, err := h.Exec(context.Background(), process.ExecSpec{Argv: []string{"sh", "-c", "head -c 20000000 /dev/zero"}})
	require.NoError(t, err)
	require.Len(t, res.Stdout, maxExecOutput)
	require.True(t, strings.HasSuffix(string(res.Stdout), "[output truncated]\n"))
	require.True(t, hub.Connected("box"), "a large result must not break the connection")
}
