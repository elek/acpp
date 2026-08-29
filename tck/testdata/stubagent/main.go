// Command stubagent is a minimal ACP agent used to exercise the TCK end to end
// without depending on a real model. It speaks just enough of the protocol to
// answer the TCK scenario, and — crucially — it persists its conversation to a
// file in the working directory so session/load can genuinely restore it after
// the process has been killed. That makes it a faithful stand-in for the resume
// behaviour the TCK is checking.
//
// The canned answers are matched against the TCK's own probe wording, so this
// file and tck.scenario move together.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// historyFile is where the conversation is persisted, relative to the working
// directory the client asked the session to run in.
const historyFile = ".stubagent-history.json"

// sessionID is fixed: the stub serves one session at a time, and the TCK resumes
// exactly the id session/new handed out.
const sessionID = "stub-session-1"

// tokenRe finds the token the TCK plants with its memo probe, so the stub can
// answer the recall probe from its restored history.
var tokenRe = regexp.MustCompile(`ACPP-TOKEN-[0-9A-Fa-f]+`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stubagent:", err)
		os.Exit(1)
	}
}

type agent struct {
	out *json.Encoder
	cwd string
}

func run() error {
	a := &agent{out: json.NewEncoder(os.Stdout)}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var msg struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return fmt.Errorf("decode %q: %w", line, err)
		}
		if err := a.handle(msg.ID, msg.Method, msg.Params); err != nil {
			return err
		}
	}
	return in.Err()
}

func (a *agent) handle(id *json.RawMessage, method string, params json.RawMessage) error {
	switch method {
	case "initialize":
		return a.result(id, map[string]any{
			"protocolVersion":   1,
			"agentCapabilities": map[string]any{"loadSession": true, "promptCapabilities": map[string]any{"image": false}},
			"agentInfo":         map[string]any{"name": "stubagent", "version": "1"},
		})

	case "session/new":
		var p struct {
			Cwd string `json:"cwd"`
		}
		_ = json.Unmarshal(params, &p)
		a.cwd = p.Cwd
		// A new session starts from an empty conversation.
		_ = os.Remove(a.historyPath())
		return a.result(id, map[string]any{"sessionId": sessionID})

	case "session/load":
		var p struct {
			Cwd       string `json:"cwd"`
			SessionId string `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &p)
		a.cwd = p.Cwd
		history, err := a.history()
		if err != nil || p.SessionId != sessionID {
			return a.fail(id, fmt.Sprintf("unknown session %q", p.SessionId))
		}
		// Replay the restored conversation before answering the load, as real
		// agents do.
		for _, turn := range history {
			a.chunk("replaying: " + turn)
		}
		return a.result(id, map[string]any{})

	case "session/prompt":
		var p struct {
			Prompt []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(params, &p)
		var text []string
		for _, block := range p.Prompt {
			text = append(text, block.Text)
		}
		prompt := strings.Join(text, "")
		history, _ := a.history()
		a.chunk(a.answer(prompt, history))
		if err := a.append(prompt); err != nil {
			return err
		}
		return a.result(id, map[string]any{"stopReason": "end_turn"})

	default:
		// Notifications (session/cancel) and anything unrecognised: nothing to do.
		if id != nil {
			return a.fail(id, "method not found: "+method)
		}
		return nil
	}
}

// answer produces the canned reply for a probe. The recall probe is the only one
// that consults history — which is exactly what makes it a resume test.
func (a *agent) answer(prompt string, history []string) string {
	switch {
	case strings.Contains(prompt, "Reply with that token"):
		for _, turn := range history {
			if tok := tokenRe.FindString(turn); tok != "" {
				return tok
			}
		}
		return "I do not remember any token."
	case strings.Contains(prompt, "capital of Spain"):
		return "Madrid"
	case strings.Contains(prompt, "files in the current working directory"):
		entries, err := os.ReadDir(a.cwd)
		if err != nil {
			return "cannot read " + a.cwd
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return strings.Join(names, "\n")
	default:
		return "OK"
	}
}

func (a *agent) historyPath() string { return filepath.Join(a.cwd, historyFile) }

// history reads the persisted conversation. A missing file is an error so
// session/load can reject a session it never created.
func (a *agent) history() ([]string, error) {
	b, err := os.ReadFile(a.historyPath())
	if err != nil {
		return nil, err
	}
	var turns []string
	if err := json.Unmarshal(b, &turns); err != nil {
		return nil, err
	}
	return turns, nil
}

// append records one prompt, creating the history file if this is the first turn.
func (a *agent) append(prompt string) error {
	turns, _ := a.history()
	turns = append(turns, prompt)
	b, err := json.Marshal(turns)
	if err != nil {
		return err
	}
	return os.WriteFile(a.historyPath(), b, 0o644)
}

// chunk emits one agent_message_chunk notification.
func (a *agent) chunk(text string) {
	_ = a.out.Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": text},
			},
		},
	})
}

func (a *agent) result(id *json.RawMessage, result any) error {
	return a.out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (a *agent) fail(id *json.RawMessage, message string) error {
	return a.out.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32603, "message": message},
	})
}
