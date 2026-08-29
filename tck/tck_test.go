package tck

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// buildStubAgent compiles testdata/stubagent and returns the binary's path.
func buildStubAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "stubagent")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/stubagent")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building stub agent: %s", out)
	return bin
}

// resultsByName indexes a report so assertions can name the check they mean.
func resultsByName(results []Result) map[string]Result {
	m := make(map[string]Result, len(results))
	for _, r := range results {
		m[r.Name] = r
	}
	return m
}

// TestRunnerResume drives the whole TCK against the stub agent, which persists
// its conversation to disk. The resume phase kills the first agent process and
// starts a second one that reloads the same ACP session, so a passing recall
// check means the token really came back through session/load.
func TestRunnerResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runner := &Runner{Agent: buildStubAgent(t), Timeout: 20 * time.Second}
	results, err := runner.Run(ctx)
	require.NoError(t, err)

	byName := resultsByName(results)

	require.True(t, byName["cap: loadSession"].OK, "stub agent advertises loadSession")
	require.True(t, byName["session resume: load"].OK,
		"session/load accepted: %s", byName["session resume: load"].Value)
	require.True(t, byName["session resume: history replayed"].OK,
		"agent replayed history on load: %s", byName["session resume: history replayed"].Value)
	require.True(t, byName["session resume: recall"].OK,
		"resumed session recalled the planted token: %s", byName["session resume: recall"].Value)

	// The first-session probes still pass, so the added memo probe did not disturb
	// the existing scenario.
	require.True(t, byName["conversation: capital"].OK, byName["conversation: capital"].Value)
	require.True(t, byName["conversation: list-dir"].OK, byName["conversation: list-dir"].Value)
	require.True(t, byName["prompt finished"].OK, byName["prompt finished"].Value)
}

func TestSupportsLoadSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps string
		want bool
	}{
		{"advertised", `{"loadSession":true}`, true},
		{"declined", `{"loadSession":false}`, false},
		{"absent", `{"promptCapabilities":{"image":true}}`, false},
		{"empty", ``, false},
		{"unparseable", `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, supportsLoadSession(json.RawMessage(tc.caps)))
		})
	}
}

// TestCheckResume covers the reporting branches that a live run cannot easily
// produce: an agent without the capability, and a rejected session/load.
func TestCheckResume(t *testing.T) {
	const secret = "ACPP-TOKEN-DEADBEEF"

	t.Run("skipped", func(t *testing.T) {
		tr := NewTranscript()
		tr.ResumeSkipped = "loadSession not advertised"
		byName := resultsByName(checkResume(tr))
		require.False(t, byName["session resume: load"].OK)
		require.Equal(t, "skipped: loadSession not advertised", byName["session resume: recall"].Value)
	})

	t.Run("load failed", func(t *testing.T) {
		tr := NewTranscript()
		tr.ResumeErr = "handshake: acp: error response for session/load"
		byName := resultsByName(checkResume(tr))
		require.False(t, byName["session resume: load"].OK)
		require.Contains(t, byName["session resume: load"].Value, "session/load")
		require.False(t, byName["session resume: recall"].OK)
	})

	t.Run("recalled", func(t *testing.T) {
		tr := NewTranscript()
		tr.Secret = secret
		tr.BeginSink("resume-replay")
		tr.Turns["resume-replay"].Text = "replaying: remember " + secret
		tr.Begin("recall")
		tr.Turns["recall"].Text = secret
		byName := resultsByName(checkResume(tr))
		require.True(t, byName["session resume: load"].OK)
		require.True(t, byName["session resume: history replayed"].OK)
		require.True(t, byName["session resume: recall"].OK)
		require.Equal(t, secret, byName["session resume: recall"].Value)
	})

	t.Run("forgot", func(t *testing.T) {
		tr := NewTranscript()
		tr.Secret = secret
		tr.Begin("recall")
		tr.Turns["recall"].Text = "I do not remember any token."
		byName := resultsByName(checkResume(tr))
		require.True(t, byName["session resume: load"].OK)
		require.False(t, byName["session resume: history replayed"].OK)
		require.False(t, byName["session resume: recall"].OK)
	})
}

// TestBeginSinkStaysOutOfProbeOrder pins the property the resume phase depends
// on: history an agent replays during session/load must not be attributed to a
// probe turn, or the recall check could pass on replayed text.
func TestBeginSinkStaysOutOfProbeOrder(t *testing.T) {
	tr := NewTranscript()
	tr.Begin("capital")
	tr.BeginSink("resume-replay")
	tr.Begin("recall")

	require.Equal(t, []string{"capital", "recall"}, tr.Order)
	require.Contains(t, tr.Turns, "resume-replay")
}
