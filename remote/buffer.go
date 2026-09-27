package remote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
)

// errOverflow is returned by outbox.Write when the unacknowledged backlog would
// exceed the limit — the peer has been gone too long, or is not keeping up.
var errOverflow = errors.New("remote: stream buffer full")

// outbox is the sending half of one direction of a stream. Bytes stay buffered
// until the peer acknowledges them, so they can be sent again after a
// reconnect. Offsets are absolute positions in the stream.
type outbox struct {
	mu    sync.Mutex
	base  uint64 // offset of buf[0]; everything before it is acknowledged
	buf   []byte
	sent  uint64 // next offset to send on the current connection
	limit int
	eof   bool // no more writes

	// sendMu serializes senders, so frames of one stream leave in order.
	sendMu sync.Mutex
}

func newOutbox(start uint64, limit int) *outbox {
	return &outbox{base: start, sent: start, limit: limit}
}

// Write appends p. It never blocks on the network.
func (o *outbox) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.eof {
		return 0, io.ErrClosedPipe
	}
	if len(o.buf)+len(p) > o.limit {
		return 0, errOverflow
	}
	o.buf = append(o.buf, p...)
	return len(p), nil
}

// closeWrite marks the end of the stream.
func (o *outbox) closeWrite() {
	o.mu.Lock()
	o.eof = true
	o.mu.Unlock()
}

// next returns the next unsent chunk and its offset, marking it sent.
func (o *outbox) next(max int) (uint64, []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	start := int(o.sent - o.base)
	n := len(o.buf) - start
	if n > max {
		n = max
	}
	if n <= 0 {
		return o.sent, nil
	}
	chunk := append([]byte(nil), o.buf[start:start+n]...)
	off := o.sent
	o.sent += uint64(n)
	return off, chunk
}

// drained reports that the stream has ended and every byte was sent.
func (o *outbox) drained() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.eof && o.sent == o.base+uint64(len(o.buf))
}

// ack records that the peer has consumed everything before off. The buffer is
// trimmed only up to the last line boundary before off, never into a line: a
// server that adopts the stream after a restart replays from the buffer start,
// and it must start at the beginning of a line (an ACP message) — the partial
// line the old server had consumed was never a complete message to it.
func (o *outbox) ack(off uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	end := o.base + uint64(len(o.buf))
	if off <= o.base || off > end {
		return
	}
	if o.sent < off {
		o.sent = off
	}
	cut := bytes.LastIndexByte(o.buf[:off-o.base], '\n')
	if cut < 0 {
		return
	}
	o.buf = append([]byte(nil), o.buf[cut+1:]...)
	o.base += uint64(cut + 1)
}

// unsend undoes a send that failed: the bytes from off on did not reach the
// peer, so the next send starts there again.
func (o *outbox) unsend(off uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if off >= o.base && off < o.sent {
		o.sent = off
	}
}

// rewind makes the next send start at off: the peer (on a new connection) holds
// exactly the bytes before it.
func (o *outbox) rewind(off uint64) error {
	o.mu.Lock()
	end := o.base + uint64(len(o.buf))
	if off < o.base || off > end {
		o.mu.Unlock()
		return fmt.Errorf("remote: cannot resume at offset %d, buffer holds [%d, %d)", off, o.base, end)
	}
	o.mu.Unlock()
	o.ack(off)
	o.mu.Lock()
	o.sent = off
	o.mu.Unlock()
	return nil
}

// sentOffset is the next offset to send.
func (o *outbox) sentOffset() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sent
}

// acked is the start of the buffer: everything before it the peer consumed. It
// is always at a line boundary.
func (o *outbox) acked() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.base
}

// inbuf is the receiving half of one direction of a stream: it reassembles
// offset-addressed frames (dropping duplicates a replay can cause) and hands
// the bytes to a reader. It buffers without bound so the connection's read loop
// never waits for a slow consumer — a consumer that itself waits on the same
// connection (an exec from inside an ACP handler) would otherwise deadlock.
type inbuf struct {
	mu   sync.Mutex
	cond *sync.Cond
	next uint64 // offset of the next byte expected from the peer
	buf  []byte
	eof  bool
	// consumed is the offset of the next byte a reader will get.
	consumed uint64
	// lineEnd reports that the received data ends a line (or nothing arrived).
	lineEnd bool
	// onConsume, when set, is told the consumed offset after each read; the
	// owner acks it to the peer. Acking consumption rather than receipt keeps
	// bytes nobody processed yet in the peer's buffer, for a server restart.
	onConsume func(off uint64)
	// drained is closed once a reader has seen EOF.
	drained     chan struct{}
	drainedOnce sync.Once
}

func newInbuf(start uint64) *inbuf {
	b := &inbuf{next: start, consumed: start, lineEnd: true, drained: make(chan struct{})}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// write accepts a frame at off. It returns the new receive offset (to ack), or
// an error for a gap, which means the peer's replay went wrong.
func (b *inbuf) write(off uint64, p []byte) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if off > b.next {
		return b.next, fmt.Errorf("remote: data gap: got offset %d, expected %d", off, b.next)
	}
	skip := b.next - off
	if skip >= uint64(len(p)) {
		return b.next, nil // duplicate of what we already hold
	}
	if !b.eof {
		b.buf = append(b.buf, p[skip:]...)
		b.cond.Broadcast()
	}
	b.next += uint64(len(p)) - skip
	b.lineEnd = p[len(p)-1] == '\n'
	return b.next, nil
}

// endsLine reports whether the received data ends at a line boundary.
func (b *inbuf) endsLine() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lineEnd
}

// received is the receive offset.
func (b *inbuf) received() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.next
}

// closeWrite ends the stream: readers get EOF once the buffer is empty.
func (b *inbuf) closeWrite() {
	b.mu.Lock()
	b.eof = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

// Read implements io.Reader.
func (b *inbuf) Read(p []byte) (int, error) {
	b.mu.Lock()
	for len(b.buf) == 0 && !b.eof {
		b.cond.Wait()
	}
	if len(b.buf) == 0 {
		b.drainedOnce.Do(func() { close(b.drained) })
		b.mu.Unlock()
		return 0, io.EOF
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	if len(b.buf) == 0 {
		b.buf = nil
	}
	b.consumed += uint64(n)
	off, notify := b.consumed, b.onConsume
	b.mu.Unlock()
	if notify != nil {
		notify(off)
	}
	return n, nil
}
