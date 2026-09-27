package remote

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Acks trim only whole lines, so the buffer always starts at a line boundary —
// where a server adopting the stream after a restart can start reading.
func TestOutboxAckTrimsWholeLines(t *testing.T) {
	o := newOutbox(0, 1<<20)
	_, err := o.Write([]byte("one\ntwo\nthr"))
	require.NoError(t, err)

	o.ack(6) // into "two"
	require.Equal(t, uint64(4), o.acked())
	o.ack(11) // into "thr", past "two\n"
	require.Equal(t, uint64(8), o.acked())

	// A resume from the peer's offset inside the line still sends only the rest.
	require.NoError(t, o.rewind(10))
	off, chunk := o.next(100)
	require.Equal(t, uint64(10), off)
	require.Equal(t, "r", string(chunk))
	require.Error(t, o.rewind(3), "before the buffer start")
}

func TestOutboxUnsend(t *testing.T) {
	o := newOutbox(0, 1<<20)
	_, _ = o.Write([]byte("abcdef"))
	off, chunk := o.next(3)
	require.Equal(t, "abc", string(chunk))
	o.unsend(off) // the send failed
	off, chunk = o.next(100)
	require.Equal(t, uint64(0), off)
	require.Equal(t, "abcdef", string(chunk))
}

func TestOutboxOverflow(t *testing.T) {
	o := newOutbox(0, 4)
	_, err := o.Write([]byte("abcde"))
	require.ErrorIs(t, err, errOverflow)
}

func TestInbufReassembles(t *testing.T) {
	b := newInbuf(10)
	var consumed []uint64
	b.onConsume = func(off uint64) { consumed = append(consumed, off) }

	next, err := b.write(10, []byte("abc"))
	require.NoError(t, err)
	require.Equal(t, uint64(13), next)
	_, err = b.write(11, []byte("bcde")) // overlaps a replay
	require.NoError(t, err)
	_, err = b.write(20, []byte("x"))
	require.Error(t, err, "gap")
	require.False(t, b.endsLine())
	_, _ = b.write(15, []byte("\n"))
	require.True(t, b.endsLine())

	b.closeWrite()
	got, err := io.ReadAll(b)
	require.NoError(t, err)
	require.Equal(t, "abcde\n", string(got))
	require.Equal(t, uint64(16), consumed[len(consumed)-1])
}
