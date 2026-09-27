package remote

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Keepalive timing. A peer silent for readTimeout is considered gone, so a
// half-open connection (a laptop that went to sleep) is detected within a minute
// rather than when TCP finally gives up.
const (
	pingInterval = 20 * time.Second
	readTimeout  = 60 * time.Second
	writeTimeout = 30 * time.Second
)

// conn wraps one websocket with what both ends need: serialized writes,
// keepalive, and asynchronous acks.
//
// Rule: the read loop never writes. A read loop blocked on a write while the
// peer's read loop is blocked on a write to us is a deadlock once both socket
// buffers fill, so everything a read loop wants to send (acks, replies) is
// handed to another goroutine.
type conn struct {
	ws *websocket.Conn

	wmu sync.Mutex

	closed    chan struct{}
	closeOnce sync.Once

	ackMu  sync.Mutex
	acks   map[ackKey]uint64
	ackSig chan struct{}
}

type ackKey struct {
	stream uuid.UUID
	kind   byte
}

func newConn(ws *websocket.Conn) *conn {
	c := &conn{
		ws:     ws,
		closed: make(chan struct{}),
		acks:   make(map[ackKey]uint64),
		ackSig: make(chan struct{}, 1),
	}
	ws.SetReadLimit(maxChunk + dataHeaderLen + 16<<20) // data frames, or an exec result
	_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(readTimeout))
	})
	go c.pinger()
	go c.acker()
	return c
}

var errClosed = errors.New("remote: connection closed")

func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.ws.Close()
	})
}

func (c *conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *conn) sendJSON(m *message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.write(websocket.TextMessage, b)
}

func (c *conn) sendData(id uuid.UUID, kind byte, off uint64, p []byte) error {
	return c.write(websocket.BinaryMessage, encodeData(id, kind, off, p))
}

func (c *conn) write(typ int, b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.isClosed() {
		return errClosed
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.ws.WriteMessage(typ, b); err != nil {
		c.close()
		return err
	}
	return nil
}

// read returns the next frame, extending the read deadline on any traffic.
func (c *conn) read() (int, []byte, error) {
	typ, b, err := c.ws.ReadMessage()
	if err != nil {
		c.close()
		return 0, nil, err
	}
	_ = c.ws.SetReadDeadline(time.Now().Add(readTimeout))
	return typ, b, nil
}

// queueAck records that a stream has been received up to off; the acker sends
// the latest offset per stream, coalescing bursts.
func (c *conn) queueAck(id uuid.UUID, kind byte, off uint64) {
	c.ackMu.Lock()
	c.acks[ackKey{id, kind}] = off
	c.ackMu.Unlock()
	select {
	case c.ackSig <- struct{}{}:
	default:
	}
}

func (c *conn) acker() {
	for {
		select {
		case <-c.closed:
			return
		case <-c.ackSig:
		}
		c.ackMu.Lock()
		acks := c.acks
		c.acks = make(map[ackKey]uint64)
		c.ackMu.Unlock()
		for k, off := range acks {
			if err := c.sendJSON(&message{Type: msgAck, Stream: k.stream.String(), Kind: k.kind, Offset: off}); err != nil {
				return
			}
		}
	}
}

func (c *conn) pinger() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				c.close()
				return
			}
		}
	}
}

// flush sends everything unsent in ob as data frames for stream id.
func (c *conn) flush(id uuid.UUID, kind byte, ob *outbox) error {
	ob.sendMu.Lock()
	defer ob.sendMu.Unlock()
	return c.flushLocked(id, kind, ob)
}

// flushLocked is flush for a caller already holding ob.sendMu.
func (c *conn) flushLocked(id uuid.UUID, kind byte, ob *outbox) error {
	for {
		off, chunk := ob.next(maxChunk)
		if len(chunk) == 0 {
			return nil
		}
		if err := c.sendData(id, kind, off, chunk); err != nil {
			ob.unsend(off)
			return err
		}
	}
}
