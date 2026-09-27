package remote

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/elek/acpp/process"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// DefaultGrace is how long both sides keep a disconnected location's processes.
const DefaultGrace = 5 * time.Minute

// DefaultBufferLimit bounds the unacknowledged bytes buffered per stream
// direction while the peer is away.
const DefaultBufferLimit = 16 << 20

// closeTimeout bounds how long Handle.Close waits for the remote process to
// exit: the remote agent's own graceful stop takes up to ~10s.
const closeTimeout = 20 * time.Second

// AdoptFunc takes back a conversation a remote agent kept running across a
// server restart, from the descriptor it kept (the router's Adopt). An error
// makes the hub tell the remote agent to stop the process.
type AdoptFunc func(ctx context.Context, host process.Host, h process.Handle, descriptor []byte) error

// FinalizeFunc records the end of a conversation the server no longer tracks:
// its process expired on the remote side while no server was connected.
type FinalizeFunc func(descriptor []byte, errMsg string)

// Hub is the server side: it accepts remote agents on Path and is the
// process.Host for each connected location.
type Hub struct {
	secret   string
	grace    time.Duration
	limit    int
	adopt    AdoptFunc
	finalize FinalizeFunc

	upgrader websocket.Upgrader

	mu     sync.Mutex
	locs   map[string]*location
	closed bool
}

// HubOption configures a Hub.
type HubOption func(*Hub)

// WithGrace overrides DefaultGrace.
func WithGrace(d time.Duration) HubOption { return func(h *Hub) { h.grace = d } }

// WithBufferLimit overrides DefaultBufferLimit.
func WithBufferLimit(n int) HubOption { return func(h *Hub) { h.limit = n } }

// WithAdopt sets the callback for conversations that outlived a server
// restart. Without it such processes are stopped.
func WithAdopt(f AdoptFunc) HubOption { return func(h *Hub) { h.adopt = f } }

// WithFinalize sets the callback for processes that expired unobserved.
func WithFinalize(f FinalizeFunc) HubOption { return func(h *Hub) { h.finalize = f } }

// NewHub creates a Hub accepting agents that present secret, which must not be
// empty.
func NewHub(secret string, opts ...HubOption) *Hub {
	h := &Hub{
		secret: secret,
		grace:  DefaultGrace,
		limit:  DefaultBufferLimit,
		locs:   make(map[string]*location),
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// SetAdopt sets the adopt callback after construction (the router it points to
// usually needs the hub first).
func (h *Hub) SetAdopt(f AdoptFunc) {
	h.mu.Lock()
	h.adopt = f
	h.mu.Unlock()
}

// SetFinalize sets the finalize callback after construction.
func (h *Hub) SetFinalize(f FinalizeFunc) {
	h.mu.Lock()
	h.finalize = f
	h.mu.Unlock()
}

// location is the server's state for one location name. It outlives its
// connection for the grace period.
type location struct {
	name string
	conn *conn // nil while disconnected
	// claimed is set while an agent is connecting, so a second one is refused.
	claimed bool
	// ready is set once the welcome has been sent: data may flow.
	ready   bool
	handles map[uuid.UUID]*handle
	// tombstones are streams the server gave up on while disconnected; the
	// remote agent is told to stop them when it returns.
	tombstones map[uuid.UUID]bool
	// finished are streams this hub saw end (exit, expiry, close), so a remote
	// agent that reports one again (its forget was lost) does not get the
	// conversation finalized a second time.
	finished map[uuid.UUID]time.Time
	grace    *time.Timer

	nextReq uint64
	reqs    map[uint64]chan *message
}

func (h *Hub) loc(name string) *location {
	l, ok := h.locs[name]
	if !ok {
		l = &location{
			name:       name,
			handles:    make(map[uuid.UUID]*handle),
			tombstones: make(map[uuid.UUID]bool),
			finished:   make(map[uuid.UUID]time.Time),
			reqs:       make(map[uint64]chan *message),
		}
		h.locs[name] = l
	}
	return l
}

// Locations returns the names of the connected locations.
func (h *Hub) Locations() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for name, l := range h.locs {
		if l.ready {
			out = append(out, name)
		}
	}
	return out
}

// Connected reports whether location has a connected remote agent.
func (h *Hub) Connected(location string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.locs[location]
	return ok && l.ready
}

// Host returns the host for a connected location. The host stays usable across
// reconnects of the same location.
func (h *Hub) Host(location string) (process.Host, error) {
	if !h.Connected(location) {
		return nil, fmt.Errorf("location %q is not connected", location)
	}
	return &host{hub: h, name: location}, nil
}

// Close drops every connection without stopping anything: the remote agents
// keep their processes for the grace period, for the next server to adopt.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	var conns []*conn
	for _, l := range h.locs {
		if l.conn != nil {
			conns = append(conns, l.conn)
		}
		if l.grace != nil {
			l.grace.Stop()
		}
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// ServeHTTP accepts a remote agent's websocket.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if h.secret == "" || subtle.ConstantTimeCompare([]byte(auth), []byte(h.secret)) != 1 {
		http.Error(w, "bad secret", http.StatusUnauthorized)
		return
	}
	if v := r.Header.Get(headerProtocol); v != ProtocolVersion {
		http.Error(w, fmt.Sprintf("unsupported protocol version %q, server speaks %q", v, ProtocolVersion), http.StatusUpgradeRequired)
		return
	}
	name := r.Header.Get(headerLocation)
	if name == "" || process.IsLocal(name) {
		http.Error(w, fmt.Sprintf("invalid location %q", name), http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	l := h.loc(name)
	if l.claimed {
		h.mu.Unlock()
		http.Error(w, fmt.Sprintf("location %q is already connected", name), http.StatusConflict)
		return
	}
	l.claimed = true
	h.mu.Unlock()

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.mu.Lock()
		l.claimed = false
		h.mu.Unlock()
		slog.Warn("remote: websocket upgrade failed", "location", name, "error", err)
		return
	}
	c := newConn(ws)
	if err := h.attach(r.Context(), l, c); err != nil {
		slog.Warn("remote: agent handshake failed", "location", name, "error", err)
		c.close()
		h.detachConn(l, c)
		return
	}
	slog.Info("remote: agent connected", "location", name, "remote_addr", r.RemoteAddr)
	h.readLoop(l, c)
	slog.Info("remote: agent disconnected", "location", name)
	h.detachConn(l, c)
}

// attach runs the hello/welcome exchange: known streams resume, unknown ones
// are adopted, tombstoned ones are stopped.
func (h *Hub) attach(ctx context.Context, l *location, c *conn) error {
	typ, b, err := c.read()
	if err != nil {
		return err
	}
	var hello message
	if typ != websocket.TextMessage || json.Unmarshal(b, &hello) != nil || hello.Type != msgHello {
		return fmt.Errorf("expected hello")
	}

	h.mu.Lock()
	l.conn = c
	if l.grace != nil {
		l.grace.Stop()
		l.grace = nil
	}
	adopt, finalize := h.adopt, h.finalize
	h.mu.Unlock()

	hostRef := &host{hub: h, name: l.name}
	seen := make(map[uuid.UUID]bool)
	var welcome message
	welcome.Type = msgWelcome
	var resumed []*handle
	for _, s := range hello.Streams {
		id, err := parseStreamID(s.ID)
		if err != nil {
			continue
		}
		seen[id] = true
		h.mu.Lock()
		hd, known := l.handles[id]
		tomb := l.tombstones[id]
		delete(l.tombstones, id)
		h.mu.Unlock()

		switch {
		case known:
			hd.in.sendMu.Lock()
			err := hd.in.rewind(s.InReceived)
			hd.in.sendMu.Unlock()
			if err != nil {
				slog.Warn("remote: cannot resume stream; stopping it", "location", l.name, "stream", s.ID, "error", err)
				hd.finish(err.Error())
				welcome.Resume = append(welcome.Resume, resumeInfo{ID: s.ID, Action: actionKill})
				continue
			}
			welcome.Resume = append(welcome.Resume, resumeInfo{ID: s.ID, Action: actionResume, OutFrom: hd.out.received()})
			resumed = append(resumed, hd)
		case tomb:
			welcome.Resume = append(welcome.Resume, resumeInfo{ID: s.ID, Action: actionKill})
		case h.wasFinished(l, id):
			// Already finalized here; the agent just did not hear our forget.
			welcome.Resume = append(welcome.Resume, resumeInfo{ID: s.ID, Action: actionKill})
		case s.Exited:
			// It ended while no server was watching; record that and let it go.
			if finalize != nil {
				finalize(s.Descriptor, "the agent process on "+l.name+" ended while the server was away: "+s.ExitError)
			}
			welcome.Resume = append(welcome.Resume, resumeInfo{ID: s.ID, Action: actionKill})
		default:
			hd := h.newHandle(l, id, s.PID, s.OutAcked, s.InReceived)
			hd.desc = s.Descriptor
			if !s.InLineEnd {
				_, _ = hd.in.Write([]byte("\n"))
			}
			ok := false
			if adopt != nil {
				if err := adopt(ctx, hostRef, hd, s.Descriptor); err != nil {
					slog.Warn("remote: cannot adopt conversation; stopping its process",
						"location", l.name, "stream", s.ID, "error", err)
				} else {
					ok = true
				}
			}
			if !ok {
				h.dropHandle(hd)
				welcome.Resume = append(welcome.Resume, resumeInfo{ID: s.ID, Action: actionKill})
				continue
			}
			welcome.Resume = append(welcome.Resume, resumeInfo{ID: s.ID, Action: actionResume, OutFrom: s.OutAcked})
			resumed = append(resumed, hd)
		}
	}

	// Streams the agent no longer has died with it (the remote agent restarted).
	h.mu.Lock()
	var lost []*handle
	for id, hd := range l.handles {
		if !seen[id] {
			lost = append(lost, hd)
		}
	}
	h.mu.Unlock()
	for _, hd := range lost {
		hd.finish("the remote agent on " + l.name + " restarted and lost the process")
	}

	if err := c.sendJSON(&welcome); err != nil {
		return err
	}
	h.mu.Lock()
	l.ready = true
	h.mu.Unlock()
	// Off the handshake path: the backlogs can be large, and the read loop has to
	// be running while they go out, or both ends block writing.
	for _, hd := range resumed {
		go func() {
			// A descriptor pushed while disconnected never arrived; resend it.
			if desc := hd.descriptor(); desc != nil {
				_ = c.sendJSON(&message{Type: msgDescriptor, Stream: hd.id.String(), Descriptor: desc})
			}
			hd.flush()
		}()
	}
	return nil
}

func (h *Hub) wasFinished(l *location, id uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := l.finished[id]
	return ok
}

// conn returns the location's connection if it is ready.
func (h *Hub) conn(name string) *conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l, ok := h.locs[name]; ok && l.ready {
		return l.conn
	}
	return nil
}

// detachConn forgets a dropped connection and starts the grace timer for the
// location's streams.
func (h *Hub) detachConn(l *location, c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l.conn == c || l.conn == nil {
		l.conn = nil
		l.ready = false
		l.claimed = false
	}
	for id, ch := range l.reqs {
		close(ch)
		delete(l.reqs, id)
	}
	if h.closed || len(l.handles) == 0 {
		return
	}
	if l.grace != nil {
		l.grace.Stop()
	}
	l.grace = time.AfterFunc(h.grace, func() { h.expire(l) })
}

// expire gives up on a location that stayed away for the whole grace period.
func (h *Hub) expire(l *location) {
	h.mu.Lock()
	if l.conn != nil || h.closed {
		h.mu.Unlock()
		return
	}
	var handles []*handle
	for id, hd := range l.handles {
		handles = append(handles, hd)
		// If the agent does come back with it (its own timer can lag, e.g. across
		// a suspend), it must stop the process, not have it adopted again.
		l.tombstones[id] = true
	}
	l.grace = nil
	h.mu.Unlock()
	msg := fmt.Sprintf("remote location %q was disconnected for more than %s", l.name, h.grace)
	for _, hd := range handles {
		hd.finish(msg)
	}
}

func (h *Hub) readLoop(l *location, c *conn) {
	for {
		typ, b, err := c.read()
		if err != nil {
			return
		}
		if typ == websocket.BinaryMessage {
			id, kind, off, p, err := decodeData(b)
			if err != nil || kind != kindStdout {
				slog.Warn("remote: bad data frame", "location", l.name, "error", err)
				c.close()
				return
			}
			h.mu.Lock()
			hd := l.handles[id]
			h.mu.Unlock()
			if hd == nil {
				continue // a stream we already let go of
			}
			if _, err := hd.out.write(off, p); err != nil {
				slog.Warn("remote: stream out of sync; reconnecting", "location", l.name, "error", err)
				c.close()
				return
			}
			continue
		}
		var m message
		if err := json.Unmarshal(b, &m); err != nil {
			slog.Warn("remote: bad control message", "location", l.name, "error", err)
			continue
		}
		h.handle(l, c, &m)
	}
}

// handle processes one control message on the read loop (so it must not write).
func (h *Hub) handle(l *location, c *conn, m *message) {
	switch m.Type {
	case msgSpawned, msgExecResult:
		h.mu.Lock()
		ch := l.reqs[m.Req]
		delete(l.reqs, m.Req)
		h.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	case msgAck:
		if hd := h.lookup(l, m.Stream); hd != nil && m.Kind == kindStdin {
			hd.in.ack(m.Offset)
		}
	case msgExit:
		hd := h.lookup(l, m.Stream)
		if hd != nil {
			hd.setStderr(m.Stderr)
			go hd.exited(m.Error)
		}
		stream := m.Stream
		go func() { _ = c.sendJSON(&message{Type: msgForget, Stream: stream}) }()
	default:
		slog.Warn("remote: unexpected message", "location", l.name, "type", m.Type)
	}
}

func (h *Hub) lookup(l *location, stream string) *handle {
	id, err := uuid.Parse(stream)
	if err != nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return l.handles[id]
}

// request sends m (a spawn or exec) and waits for its reply.
func (h *Hub) request(ctx context.Context, name string, m *message) (*message, error) {
	h.mu.Lock()
	l, ok := h.locs[name]
	if !ok || !l.ready {
		h.mu.Unlock()
		return nil, fmt.Errorf("location %q is not connected", name)
	}
	c := l.conn
	l.nextReq++
	m.Req = l.nextReq
	ch := make(chan *message, 1)
	l.reqs[m.Req] = ch
	h.mu.Unlock()

	if err := c.sendJSON(m); err != nil {
		h.mu.Lock()
		delete(l.reqs, m.Req)
		h.mu.Unlock()
		return nil, fmt.Errorf("location %q: %w", name, err)
	}
	select {
	case reply, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("location %q disconnected", name)
		}
		return reply, nil
	case <-ctx.Done():
		h.mu.Lock()
		delete(l.reqs, m.Req)
		h.mu.Unlock()
		return nil, ctx.Err()
	}
}

// send delivers a control message to a location if it is connected.
func (h *Hub) send(name string, m *message) bool {
	h.mu.Lock()
	l, ok := h.locs[name]
	var c *conn
	if ok && l.ready {
		c = l.conn
	}
	h.mu.Unlock()
	return c != nil && c.sendJSON(m) == nil
}

func (h *Hub) newHandle(l *location, id uuid.UUID, pid int, outFrom, inFrom uint64) *handle {
	hd := &handle{
		hub:  h,
		loc:  l.name,
		id:   id,
		pid:  pid,
		in:   newOutbox(inFrom, h.limit),
		out:  newInbuf(outFrom),
		done: make(chan struct{}),
	}
	hd.out.onConsume = func(off uint64) {
		if c := h.conn(hd.loc); c != nil {
			c.queueAck(id, kindStdout, off)
		}
	}
	h.mu.Lock()
	l.handles[id] = hd
	h.mu.Unlock()
	return hd
}

// finishedRetention bounds how long finished stream ids are remembered: well
// past any grace period after which an agent could still report one.
const finishedRetention = time.Hour

func (h *Hub) dropHandle(hd *handle) {
	h.mu.Lock()
	if l, ok := h.locs[hd.loc]; ok && l.handles[hd.id] == hd {
		delete(l.handles, hd.id)
	}
	h.mu.Unlock()
}

// markFinished remembers that a stream ended here.
func (h *Hub) markFinished(hd *handle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.locs[hd.loc]
	if !ok {
		return
	}
	now := time.Now()
	for id, at := range l.finished {
		if now.Sub(at) > finishedRetention {
			delete(l.finished, id)
		}
	}
	l.finished[hd.id] = now
}

// tombstone makes the remote agent stop the stream when it reconnects.
func (h *Hub) tombstone(hd *handle) {
	h.mu.Lock()
	if l, ok := h.locs[hd.loc]; ok {
		l.tombstones[hd.id] = true
	}
	h.mu.Unlock()
}

// host is the process.Host for one location.
type host struct {
	hub  *Hub
	name string
}

func (r *host) Name() string { return r.name }

func (r *host) Start(ctx context.Context, spec process.Spec) (process.Handle, error) {
	id, err := parseStreamID(spec.ConversationID)
	if err != nil {
		return nil, err
	}
	if spec.Sandbox != nil {
		return nil, fmt.Errorf("location %q: a sandbox built on this machine cannot run remotely", r.name)
	}
	r.hub.mu.Lock()
	l, ok := r.hub.locs[r.name]
	r.hub.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("location %q is not connected", r.name)
	}
	// Register first: the agent may write before we see the spawned reply.
	hd := r.hub.newHandle(l, id, 0, 0, 0)
	reply, err := r.hub.request(ctx, r.name, &message{
		Type:   msgSpawn,
		Stream: id.String(),
		Spawn: &spawnRequest{
			Agent:      spec.Agent,
			Cwd:        spec.Cwd,
			Env:        spec.Env,
			Sandbox:    spec.SandboxSpec,
			Descriptor: spec.Descriptor,
		},
	})
	if err != nil {
		// The spawn may or may not have happened; make sure it does not linger.
		r.hub.dropHandle(hd)
		r.hub.tombstone(hd)
		return nil, err
	}
	if reply.Error != "" {
		r.hub.dropHandle(hd)
		return nil, fmt.Errorf("location %q: %s", r.name, reply.Error)
	}
	hd.pid = reply.PID
	return hd, nil
}

func (r *host) Exec(ctx context.Context, spec process.ExecSpec) (process.ExecResult, error) {
	if dl, ok := ctx.Deadline(); ok && spec.TimeoutMs == 0 {
		spec.TimeoutMs = max(time.Until(dl).Milliseconds(), 1)
	}
	reply, err := r.hub.request(ctx, r.name, &message{Type: msgExec, Exec: &spec})
	if err != nil {
		return process.ExecResult{}, err
	}
	if reply.Error != "" {
		return process.ExecResult{}, fmt.Errorf("location %q: %s", r.name, reply.Error)
	}
	if reply.Result == nil {
		return process.ExecResult{}, fmt.Errorf("location %q: empty exec result", r.name)
	}
	return *reply.Result, nil
}

// handle is a process running on a remote agent.
type handle struct {
	hub *Hub
	loc string
	id  uuid.UUID
	pid int

	in  *outbox // stdin
	out *inbuf  // stdout

	mu     sync.Mutex
	stderr string
	// desc is the latest descriptor, kept so it can be resent after a reconnect.
	desc []byte

	done     chan struct{}
	doneOnce sync.Once
}

var (
	_ process.Handle     = (*handle)(nil)
	_ process.Detachable = (*handle)(nil)
)

func (hd *handle) Stdio() (io.WriteCloser, io.Reader) { return stdinWriter{hd}, hd.out }
func (hd *handle) PID() int                           { return hd.pid }
func (hd *handle) Done() <-chan struct{}              { return hd.done }

func (hd *handle) Stderr() string {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	return hd.stderr
}

func (hd *handle) setStderr(s string) {
	hd.mu.Lock()
	hd.stderr = strings.TrimSpace(s)
	hd.mu.Unlock()
}

func (hd *handle) isDone() bool {
	select {
	case <-hd.done:
		return true
	default:
		return false
	}
}

// flush sends buffered stdin if the location is connected.
func (hd *handle) flush() {
	hd.hub.mu.Lock()
	l, ok := hd.hub.locs[hd.loc]
	var c *conn
	if ok && l.ready {
		c = l.conn
	}
	hd.hub.mu.Unlock()
	if c != nil {
		_ = c.flush(hd.id, kindStdin, hd.in)
	}
}

// exited handles the remote exit: stdout is complete. Done closes once the
// reader drained it (bounded), mirroring a local process whose pipe is read to
// EOF before Wait returns.
func (hd *handle) exited(errMsg string) {
	hd.out.closeWrite()
	select {
	case <-hd.out.drained:
	case <-time.After(2 * time.Second):
	}
	if errMsg != "" {
		slog.Info("remote: agent process exited", "location", hd.loc, "stream", hd.id, "error", errMsg)
	}
	hd.finish("")
}

// finish ends the handle: no more data, Done closed, forgotten by the hub.
func (hd *handle) finish(errMsg string) {
	if errMsg != "" {
		hd.mu.Lock()
		if hd.stderr == "" {
			hd.stderr = errMsg
		}
		hd.mu.Unlock()
		slog.Warn("remote: agent process lost", "location", hd.loc, "stream", hd.id, "reason", errMsg)
	}
	hd.out.closeWrite()
	hd.in.closeWrite()
	hd.hub.dropHandle(hd)
	hd.hub.markFinished(hd)
	hd.doneOnce.Do(func() { close(hd.done) })
}

func (hd *handle) descriptor() []byte {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	return hd.desc
}

// Close stops the remote process gracefully and waits for its exit.
func (hd *handle) Close() {
	if hd.isDone() {
		return
	}
	if !hd.hub.send(hd.loc, &message{Type: msgClose, Stream: hd.id.String()}) {
		// Disconnected: stop it when the agent returns.
		hd.hub.tombstone(hd)
		hd.finish("")
		return
	}
	select {
	case <-hd.done:
	case <-time.After(closeTimeout):
		hd.hub.tombstone(hd)
		hd.finish("the remote process did not exit in time")
	}
}

// UpdateDescriptor implements process.Detachable.
func (hd *handle) UpdateDescriptor(desc []byte) error {
	hd.mu.Lock()
	hd.desc = desc
	hd.mu.Unlock()
	if !hd.hub.send(hd.loc, &message{Type: msgDescriptor, Stream: hd.id.String(), Descriptor: desc}) {
		return fmt.Errorf("location %q is not connected", hd.loc)
	}
	return nil
}

// Detach implements process.Detachable: the hub forgets the stream without
// stopping it, so the remote agent keeps it for the next server.
func (hd *handle) Detach() {
	hd.hub.dropHandle(hd)
	hd.out.closeWrite()
}

// stdinWriter is the agent's stdin: writes are buffered (and survive a
// reconnect) and sent when connected. Close is a no-op; the process lifecycle
// is Handle.Close.
type stdinWriter struct{ hd *handle }

func (w stdinWriter) Write(p []byte) (int, error) {
	if w.hd.isDone() {
		return 0, io.ErrClosedPipe
	}
	n, err := w.hd.in.Write(p)
	if err != nil {
		return n, fmt.Errorf("location %q: %w", w.hd.loc, err)
	}
	w.hd.flush()
	return n, nil
}

func (w stdinWriter) Close() error { return nil }
