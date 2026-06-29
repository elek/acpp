package hook

import (
	"testing"

	"github.com/elek/acpp/acp"
	"github.com/stretchr/testify/require"
)

func longPrompt() acp.PromptRequest {
	return acp.PromptRequest{Prompt: []acp.ContentBlock{
		acp.TextBlock("please refactor the whole module and add tests, this is definitely more than one hundred characters long for sure"),
	}}
}

// ctxCapturing returns a HookContext whose Trigger records the prompts it gets.
func ctxCapturing(dirty bool, triggered *[]string) HookContext {
	return HookContext{
		CWD: "/repo",
		Trigger: func(prompt string) error {
			*triggered = append(*triggered, prompt)
			return nil
		},
	}
}

func TestCommitHook_TriggersCommitThenAmend(t *testing.T) {
	var triggered []string
	hc := ctxCapturing(true, &triggered)
	h := &CommitHook{isDirty: func(string) bool { return true }}

	// First substantive turn -> commit.
	h.Outgoing(hc, longPrompt())
	h.Incoming(hc, acp.PromptResponse{})
	require.Equal(t, []string{"commit"}, triggered)

	// Second substantive turn -> commit amend.
	h.Outgoing(hc, longPrompt())
	h.Incoming(hc, acp.PromptResponse{})
	require.Equal(t, []string{"commit", "commit amend"}, triggered)
}

func TestCommitHook_SkipsShortPrompt(t *testing.T) {
	var triggered []string
	hc := ctxCapturing(true, &triggered)
	h := &CommitHook{isDirty: func(string) bool { return true }}

	h.Outgoing(hc, acp.PromptRequest{Prompt: []acp.ContentBlock{acp.TextBlock("yes")}})
	h.Incoming(hc, acp.PromptResponse{})
	require.Empty(t, triggered)
}

func TestCommitHook_SkipsCleanTree(t *testing.T) {
	var triggered []string
	hc := ctxCapturing(false, &triggered)
	h := &CommitHook{isDirty: func(string) bool { return false }}

	h.Outgoing(hc, longPrompt())
	h.Incoming(hc, acp.PromptResponse{})
	require.Empty(t, triggered)
}

func TestCommitHook_IgnoresNonResponseMessages(t *testing.T) {
	var triggered []string
	hc := ctxCapturing(true, &triggered)
	h := &CommitHook{isDirty: func(string) bool { return true }}

	h.Outgoing(hc, longPrompt())
	// A non-response message should not trigger anything.
	require.NotNil(t, h.Incoming(hc, acp.SessionNotification{}))
	require.Empty(t, triggered)
}

func TestCommitHook_PassesMessagesThrough(t *testing.T) {
	hc := HookContext{Trigger: func(string) error { return nil }}
	h := NewCommitHook()
	req := longPrompt()
	require.Equal(t, req, h.Outgoing(hc, req))
	resp := acp.PromptResponse{StopReason: acp.StopReasonEndTurn}
	require.Equal(t, resp, h.Incoming(hc, resp))
}
