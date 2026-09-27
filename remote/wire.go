// Package remote runs agents on other machines. A remote agent (Agent, run by
// `acpp remote`) dials the server's Hub over a websocket, authenticates with a
// shared secret and announces its location. The Hub then serves as the
// process.Host for that location: it asks the agent to spawn ACP agent
// processes, streams their stdin/stdout, and runs short commands on the
// agent's machine.
//
// Every process stream is offset-addressed in both directions and the receiver
// acknowledges what it holds, so after a dropped connection each side replays
// exactly what the other is missing. Both sides keep a process alive for a
// grace period while disconnected. A remote agent also keeps an opaque
// descriptor per process, which lets a restarted server adopt the conversation.
//
// See docs/plans/2026-09-27-remote-agents-design.md.
package remote

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/elek/acpp/process"
	"github.com/google/uuid"
)

// Path is where the Hub serves the websocket.
const Path = "/remote/ws"

// ProtocolVersion is bumped on any incompatible wire change.
const ProtocolVersion = "1"

// Handshake headers of the websocket upgrade request.
const (
	headerLocation = "X-Acpp-Location"
	headerProtocol = "X-Acpp-Protocol"
)

// Stream kinds carried in data frames and acks.
const (
	kindStdin  byte = 0 // server -> remote
	kindStdout byte = 1 // remote -> server
)

// Control message types (JSON text frames).
const (
	msgHello      = "hello"       // remote -> server: live streams, first message
	msgWelcome    = "welcome"     // server -> remote: what to do with each stream
	msgSpawn      = "spawn"       // server -> remote
	msgSpawned    = "spawned"     // remote -> server
	msgExec       = "exec"        // server -> remote
	msgExecResult = "exec_result" // remote -> server
	msgClose      = "close"       // server -> remote: stop a process gracefully
	msgExit       = "exit"        // remote -> server: process exited, after all its stdout
	msgForget     = "forget"      // server -> remote: exit received, drop the record
	msgDescriptor = "descriptor"  // server -> remote: replace a stream's descriptor
	msgAck        = "ack"         // both: bytes of a stream received so far
)

// Welcome actions for a stream listed in hello.
const (
	actionResume = "resume"
	actionKill   = "kill"
)

// message is the envelope of every control message; which fields are set
// depends on Type.
type message struct {
	Type   string `json:"type"`
	Stream string `json:"stream,omitempty"`
	Req    uint64 `json:"req,omitempty"`
	Error  string `json:"error,omitempty"`

	Spawn  *spawnRequest       `json:"spawn,omitempty"`
	Exec   *process.ExecSpec   `json:"exec,omitempty"`
	Result *process.ExecResult `json:"result,omitempty"`
	PID    int                 `json:"pid,omitempty"`
	Stderr string              `json:"stderr,omitempty"`

	Descriptor []byte `json:"descriptor,omitempty"`

	Kind   byte   `json:"kind,omitempty"`
	Offset uint64 `json:"offset,omitempty"`

	Streams []streamInfo `json:"streams,omitempty"`
	Resume  []resumeInfo `json:"resume,omitempty"`
}

type spawnRequest struct {
	Agent      string              `json:"agent"`
	Cwd        string              `json:"cwd,omitempty"`
	Env        []string            `json:"env,omitempty"`
	Sandbox    process.SandboxSpec `json:"sandbox"`
	Descriptor []byte              `json:"descriptor,omitempty"`
}

// streamInfo describes one of the remote agent's streams in hello.
type streamInfo struct {
	ID         string `json:"id"`
	PID        int    `json:"pid"`
	Descriptor []byte `json:"descriptor,omitempty"`
	// OutAcked is how much stdout the previous server acknowledged.
	OutAcked uint64 `json:"out_acked"`
	// InReceived is how much stdin the remote agent holds.
	InReceived uint64 `json:"in_received"`
	// InLineEnd reports that the stdin received so far ends a line. When it does
	// not, a server adopting the stream terminates the cut-off line first, so its
	// own messages are not glued to a fragment.
	InLineEnd bool   `json:"in_line_end"`
	Exited    bool   `json:"exited,omitempty"`
	ExitError string `json:"exit_error,omitempty"`
}

// resumeInfo is the server's answer for one stream of hello.
type resumeInfo struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	// OutFrom is the stdout offset the server holds; replay from there.
	OutFrom uint64 `json:"out_from"`
}

// dataHeaderLen is the binary frame header: stream UUID, offset, kind.
const dataHeaderLen = 16 + 8 + 1

// maxChunk bounds the payload of one data frame.
const maxChunk = 64 << 10

func encodeData(id uuid.UUID, kind byte, off uint64, p []byte) []byte {
	b := make([]byte, dataHeaderLen+len(p))
	copy(b, id[:])
	binary.BigEndian.PutUint64(b[16:], off)
	b[24] = kind
	copy(b[dataHeaderLen:], p)
	return b
}

func decodeData(b []byte) (id uuid.UUID, kind byte, off uint64, p []byte, err error) {
	if len(b) < dataHeaderLen {
		return id, 0, 0, nil, errors.New("remote: short data frame")
	}
	copy(id[:], b[:16])
	off = binary.BigEndian.Uint64(b[16:])
	kind = b[24]
	return id, kind, off, b[dataHeaderLen:], nil
}

func parseStreamID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return id, fmt.Errorf("remote: stream id %q is not a UUID: %w", s, err)
	}
	return id, nil
}
