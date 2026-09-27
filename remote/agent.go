package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/elek/acpp/process"
	"github.com/elek/acpp/sandbox"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ErrFatal wraps connection errors retrying cannot fix (bad secret, protocol
// mismatch); Agent.Run returns them instead of reconnecting.
var ErrFatal = errors.New("remote: fatal")

// Agent is the remote side: it keeps a connection to the server's Hub and runs
// the processes and commands the server asks for on this machine.
type Agent struct {
	// Server is the server's base URL (http, https, ws or wss).
	Server string
	Secret string
	// Location is the name this machine registers under.
	Location string
	// ResolveAgent maps a bare agent name to a command on this machine (the
	// agent_path lookup); nil leaves it unchanged.
	ResolveAgent func(string) string
	// Grace is how long processes survive without a server (DefaultGrace).
	Grace time.Duration
	// BufferLimit bounds unacknowledged stdout per process (DefaultBufferLimit).
	BufferLimit int
	// Dialer overrides websocket.DefaultDialer (tests).
	Dialer *websocket.Dialer

	procs *process.Manager
	// life is the context processes are bound to: it outlives connections and
	// ends with Run.
	life context.Context

	mu      sync.Mutex
	streams map[uuid.UUID]*stream
	grace   *time.Timer
	cur     *conn // the current connection, nil while disconnected
	// lostAt is the wall-clock time the last connection was lost. The grace
	// timer runs on the monotonic clock, which stops while the machine is
	// suspended; this catches a grace period that passed during a suspend.
	lostAt time.Time
}

// stream is one agent process and everything needed to hand it to a server.
type stream struct {
	id      uuid.UUID
	pid     int
	proc    *process.Process
	sandbox sandbox.Sandbox

	out *outbox // stdout
	in  *inbuf  // stdin

	mu         sync.Mutex
	descriptor []byte
	exited     bool
	exitErr    string
	// exitSentOn is the connection the exit message went out on; after a
	// reconnect it is sent again. Guarded by out.sendMu.
	exitSentOn *conn
	// pumped is closed once stdout is drained and the exit recorded (and sent,
	// if connected).
	pumped chan struct{}
}

func (a *Agent) defaults() {
	if a.Grace == 0 {
		a.Grace = DefaultGrace
	}
	if a.BufferLimit == 0 {
		a.BufferLimit = DefaultBufferLimit
	}
	if a.Dialer == nil {
		a.Dialer = websocket.DefaultDialer
	}
	if a.procs == nil {
		a.procs = process.NewManager()
	}
	if a.streams == nil {
		a.streams = make(map[uuid.UUID]*stream)
	}
}

// Run connects and serves until ctx ends, reconnecting with backoff. On return
// every process has been stopped.
func (a *Agent) Run(ctx context.Context) error {
	a.defaults()
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.life = life
	defer a.shutdown()

	wsURL, err := websocketURL(a.Server)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFatal, err)
	}
	backoff := time.Second
	for {
		connected, err := a.session(ctx, wsURL)
		if errors.Is(err, ErrFatal) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if connected {
			backoff = time.Second
		}
		slog.Warn("remote: connection to server lost; retrying", "server", a.Server, "error", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// session runs one connection. connected reports whether the handshake got
// through (so backoff can reset).
func (a *Agent) session(ctx context.Context, wsURL string) (connected bool, err error) {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+a.Secret)
	hdr.Set(headerLocation, a.Location)
	hdr.Set(headerProtocol, ProtocolVersion)
	ws, resp, err := a.Dialer.DialContext(ctx, wsURL, hdr)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			reason := strings.TrimSpace(string(body))
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusUpgradeRequired, http.StatusBadRequest, http.StatusNotFound:
				return false, fmt.Errorf("%w: server refused the connection (%s): %s", ErrFatal, resp.Status, reason)
			}
			return false, fmt.Errorf("server refused the connection (%s): %s", resp.Status, reason)
		}
		return false, err
	}
	c := newConn(ws)
	defer func() {
		c.close()
		a.disconnected(c)
	}()
	// When ctx ends, stop the processes while still connected, so the server
	// hears their exits now rather than after its grace period; then unblock
	// the read below.
	stop := context.AfterFunc(ctx, func() {
		a.shutdown()
		c.close()
	})
	defer stop()

	a.expireIfOverdue()
	if err := c.sendJSON(a.hello()); err != nil {
		return false, err
	}
	typ, b, err := c.read()
	if err != nil {
		return false, err
	}
	var welcome message
	if typ != websocket.TextMessage || json.Unmarshal(b, &welcome) != nil || welcome.Type != msgWelcome {
		return false, errors.New("remote: expected welcome")
	}
	a.welcome(c, &welcome)
	slog.Info("remote: connected to server", "server", a.Server, "location", a.Location)

	for {
		typ, b, err := c.read()
		if err != nil {
			return true, err
		}
		if typ == websocket.BinaryMessage {
			id, kind, off, p, err := decodeData(b)
			if err != nil || kind != kindStdin {
				return true, fmt.Errorf("remote: bad data frame: %v", err)
			}
			s := a.stream(id)
			if s == nil {
				continue
			}
			if _, err := s.in.write(off, p); err != nil {
				return true, err
			}
			continue
		}
		var m message
		if err := json.Unmarshal(b, &m); err != nil {
			slog.Warn("remote: bad control message", "error", err)
			continue
		}
		a.handle(c, &m)
	}
}

func (a *Agent) hello() *message {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := &message{Type: msgHello}
	for _, s := range a.streams {
		s.mu.Lock()
		m.Streams = append(m.Streams, streamInfo{
			ID:         s.id.String(),
			PID:        s.pid,
			Descriptor: s.descriptor,
			OutAcked:   s.out.acked(),
			InReceived: s.in.received(),
			InLineEnd:  s.in.endsLine(),
			Exited:     s.exited,
			ExitError:  s.exitErr,
		})
		s.mu.Unlock()
	}
	return m
}

// welcome applies the server's answer to hello and resumes streaming.
func (a *Agent) welcome(c *conn, w *message) {
	var resume []*stream
	for _, r := range w.Resume {
		id, err := uuid.Parse(r.ID)
		if err != nil {
			continue
		}
		s := a.stream(id)
		if s == nil {
			continue
		}
		if r.Action != actionResume {
			a.kill(s, "the server does not know this conversation")
			continue
		}
		s.out.sendMu.Lock()
		err = s.out.rewind(r.OutFrom)
		s.out.sendMu.Unlock()
		if err != nil {
			a.kill(s, err.Error())
			continue
		}
		resume = append(resume, s)
	}

	a.mu.Lock()
	a.cur = c
	a.lostAt = time.Time{}
	if a.grace != nil {
		a.grace.Stop()
		a.grace = nil
	}
	a.mu.Unlock()
	// Off the read loop: a backlog can be large, and the server must be read
	// while it goes out.
	for _, s := range resume {
		go a.flush(c, s)
	}
}

// expireIfOverdue stops the processes, before telling a server about them, if
// the connection has been lost for longer than the grace period by the wall
// clock, and waits (bounded) until their exits are recorded.
func (a *Agent) expireIfOverdue() {
	a.mu.Lock()
	overdue := a.cur == nil && !a.lostAt.IsZero() && time.Since(a.lostAt) > a.Grace
	var streams []*stream
	for _, s := range a.streams {
		streams = append(streams, s)
	}
	a.mu.Unlock()
	if !overdue {
		return
	}
	a.expire()
	for _, s := range streams {
		select {
		case <-s.pumped:
		case <-time.After(15 * time.Second):
		}
	}
}

// disconnected starts the grace period for the processes of a lost connection.
func (a *Agent) disconnected(c *conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cur != c {
		return
	}
	a.cur = nil
	// Round(0) drops the monotonic reading, so time.Since uses the wall clock.
	a.lostAt = time.Now().Round(0)
	if len(a.streams) == 0 {
		return
	}
	if a.grace == nil {
		a.grace = time.AfterFunc(a.Grace, a.expire)
	}
}

// expire stops every process after the server stayed away for the grace
// period. The records stay (as exited) so the next server hears about them.
func (a *Agent) expire() {
	a.mu.Lock()
	if a.cur != nil {
		a.mu.Unlock()
		return
	}
	a.grace = nil
	var procs []*stream
	for _, s := range a.streams {
		procs = append(procs, s)
	}
	a.mu.Unlock()
	slog.Warn("remote: no server for the whole grace period; stopping agent processes", "grace", a.Grace, "count", len(procs))
	for _, s := range procs {
		s.mu.Lock()
		if s.exitErr == "" {
			s.exitErr = fmt.Sprintf("no server connected for %s", a.Grace)
		}
		s.mu.Unlock()
		if s.proc != nil {
			go s.proc.Close()
		}
	}
}

// shutdown stops every process when the agent itself exits and waits (bounded)
// until their exits went out to the server, if one is connected.
func (a *Agent) shutdown() {
	a.mu.Lock()
	var procs []*stream
	for _, s := range a.streams {
		procs = append(procs, s)
	}
	if a.grace != nil {
		a.grace.Stop()
	}
	a.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range procs {
		if s.proc == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.proc.Close()
			select {
			case <-s.pumped:
			case <-time.After(2 * time.Second):
			}
		}()
	}
	wg.Wait()
}

func (a *Agent) stream(id uuid.UUID) *stream {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.streams[id]
}

func (a *Agent) current() *conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cur
}

// handle processes a control message on the read loop, so anything that writes
// or blocks runs on its own goroutine.
func (a *Agent) handle(c *conn, m *message) {
	switch m.Type {
	case msgSpawn:
		go a.spawn(c, m)
	case msgExec:
		go a.exec(c, m)
	case msgClose:
		if s := a.streamByName(m.Stream); s != nil && s.proc != nil {
			go s.proc.Close()
		}
	case msgDescriptor:
		if s := a.streamByName(m.Stream); s != nil {
			s.mu.Lock()
			s.descriptor = m.Descriptor
			s.mu.Unlock()
		}
	case msgForget:
		if s := a.streamByName(m.Stream); s != nil {
			a.forget(s)
		}
	case msgAck:
		if s := a.streamByName(m.Stream); s != nil && m.Kind == kindStdout {
			s.out.ack(m.Offset)
		}
	default:
		slog.Warn("remote: unexpected message", "type", m.Type)
	}
}

func (a *Agent) streamByName(name string) *stream {
	id, err := uuid.Parse(name)
	if err != nil {
		return nil
	}
	return a.stream(id)
}

func (a *Agent) spawn(c *conn, m *message) {
	reply := &message{Type: msgSpawned, Req: m.Req, Stream: m.Stream}
	pid, err := a.start(m)
	if err != nil {
		reply.Error = err.Error()
	}
	reply.PID = pid
	_ = c.sendJSON(reply)
}

func (a *Agent) start(m *message) (int, error) {
	if m.Spawn == nil {
		return 0, errors.New("spawn: missing request")
	}
	id, err := parseStreamID(m.Stream)
	if err != nil {
		return 0, err
	}
	if a.stream(id) != nil {
		return 0, fmt.Errorf("spawn: stream %s already exists", id)
	}
	req := m.Spawn
	agent := req.Agent
	if a.ResolveAgent != nil {
		agent = a.ResolveAgent(agent)
	}
	sb, err := req.Sandbox.Resolve(req.Cwd)
	if err != nil {
		return 0, err
	}
	proc, err := a.procs.Start(a.life, process.Spec{
		Agent:   agent,
		Cwd:     req.Cwd,
		Env:     req.Env,
		Sandbox: sb,
	})
	if err != nil {
		return 0, err
	}
	s := &stream{
		id:         id,
		pid:        proc.PID(),
		proc:       proc,
		sandbox:    sb,
		out:        newOutbox(0, a.BufferLimit),
		in:         newInbuf(0),
		descriptor: req.Descriptor,
		pumped:     make(chan struct{}),
	}
	s.in.onConsume = func(off uint64) {
		if c := a.current(); c != nil {
			c.queueAck(id, kindStdin, off)
		}
	}
	a.mu.Lock()
	a.streams[id] = s
	a.mu.Unlock()
	slog.Info("remote: started agent process", "stream", id, "pid", s.pid, "cwd", req.Cwd)

	go a.pumpStdin(s)
	go a.pumpStdout(s)
	return s.pid, nil
}

func (a *Agent) pumpStdin(s *stream) {
	_, _ = io.Copy(s.proc.Stdin, s.in)
}

// pumpStdout buffers the process's stdout and streams it to the current server;
// at EOF it records the exit.
func (a *Agent) pumpStdout(s *stream) {
	defer close(s.pumped)
	buf := make([]byte, 32<<10)
	for {
		n, err := s.proc.Stdout.Read(buf)
		if n > 0 {
			if _, werr := s.out.Write(buf[:n]); werr != nil {
				slog.Warn("remote: stdout backlog too large; stopping the process", "stream", s.id, "error", werr)
				s.mu.Lock()
				s.exitErr = "stdout backlog exceeded while the server was away"
				s.mu.Unlock()
				go s.proc.Close()
				break
			}
			if c := a.current(); c != nil {
				a.flush(c, s)
			}
		}
		if err != nil {
			break
		}
	}
	// Drain the rest so the process is not blocked writing to a full pipe.
	_, _ = io.Copy(io.Discard, s.proc.Stdout)
	<-s.proc.Done()
	// Record the exit before closing stdout: a concurrent flush sends the exit
	// as soon as stdout is drained, and must see the error text.
	s.mu.Lock()
	s.exited = true
	if s.exitErr == "" && s.proc.Stderr() != "" {
		s.exitErr = lastLine(s.proc.Stderr())
	}
	s.mu.Unlock()
	s.in.closeWrite()
	s.out.closeWrite()
	slog.Info("remote: agent process exited", "stream", s.id, "pid", s.pid)
	if c := a.current(); c != nil {
		a.flush(c, s)
	}
}

// flush sends a stream's unsent stdout, then — once it has all gone out — its
// exit.
func (a *Agent) flush(c *conn, s *stream) {
	s.out.sendMu.Lock()
	defer s.out.sendMu.Unlock()
	if err := c.flushLocked(s.id, kindStdout, s.out); err != nil {
		return
	}
	if !s.out.drained() || s.exitSentOn == c {
		return
	}
	s.mu.Lock()
	exitErr := s.exitErr
	s.mu.Unlock()
	stderr := ""
	if s.proc != nil {
		stderr = s.proc.Stderr()
	}
	if c.sendJSON(&message{Type: msgExit, Stream: s.id.String(), Error: exitErr, Stderr: tail(stderr, 4096)}) == nil {
		s.exitSentOn = c
	}
}

// kill stops a stream the server does not want and forgets it.
func (a *Agent) kill(s *stream, reason string) {
	slog.Info("remote: stopping agent process", "stream", s.id, "reason", reason)
	a.forget(s)
	if s.proc != nil {
		go s.proc.Close()
	}
}

func (a *Agent) forget(s *stream) {
	a.mu.Lock()
	if a.streams[s.id] == s {
		delete(a.streams, s.id)
	}
	a.mu.Unlock()
}

func (a *Agent) exec(c *conn, m *message) {
	reply := &message{Type: msgExecResult, Req: m.Req}
	if m.Exec == nil {
		reply.Error = "exec: missing request"
		_ = c.sendJSON(reply)
		return
	}
	spec := *m.Exec
	if spec.SessionSandbox != "" {
		s := a.streamByName(spec.SessionSandbox)
		if s == nil {
			reply.Error = fmt.Sprintf("exec: unknown conversation %s", spec.SessionSandbox)
			_ = c.sendJSON(reply)
			return
		}
		spec.Sandbox = s.sandbox
	}
	ctx := a.life
	if spec.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	res, err := process.RunLocal(ctx, spec)
	if err != nil {
		reply.Error = err.Error()
	} else {
		res.Stdout = truncateOutput(res.Stdout)
		res.Stderr = truncateOutput(res.Stderr)
		reply.Result = &res
	}
	_ = c.sendJSON(reply)
}

// maxExecOutput caps each output stream of an exec result, keeping the result
// (base64 in JSON) well inside the connection's message size limit.
const maxExecOutput = 1 << 20

func truncateOutput(b []byte) []byte {
	if len(b) <= maxExecOutput {
		return b
	}
	const marker = "\n[output truncated]\n"
	return append(b[:maxExecOutput-len(marker):maxExecOutput-len(marker)], marker...)
}

// websocketURL turns the server's base URL into the websocket endpoint URL.
func websocketURL(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("bad server URL %q: %w", server, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("bad server URL %q: scheme must be http, https, ws or wss", server)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + Path
	return u.String(), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
