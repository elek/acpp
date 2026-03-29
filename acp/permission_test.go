package acp

import (
	"context"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/elek/acpp/config"
	"github.com/stretchr/testify/require"
)

func approveOption() acp.PermissionOption {
	return acp.PermissionOption{OptionId: "allow", Kind: acp.PermissionOptionKindAllowOnce}
}

func rejectOption() acp.PermissionOption {
	return acp.PermissionOption{OptionId: "deny", Kind: acp.PermissionOptionKindRejectOnce}
}

func makeRequest(title string, input map[string]interface{}) acp.RequestPermissionRequest {
	t := title
	return acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			Title:    &t,
			RawInput: input,
		},
		Options: []acp.PermissionOption{approveOption(), rejectOption()},
	}
}

func TestAllowAll(t *testing.T) {
	resp, err := AllowAll(context.Background(), makeRequest("Bash", nil))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("allow"), resp.Outcome.Selected.OptionId)
}

func TestAllowAll_NoOptions(t *testing.T) {
	resp, err := AllowAll(context.Background(), acp.RequestPermissionRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Cancelled)
}

func TestNewPermissionHandler_NoRules(t *testing.T) {
	handler := NewPermissionHandler(&config.Config{}, nil)
	resp, err := handler(context.Background(), makeRequest("Bash", nil))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("allow"), resp.Outcome.Selected.OptionId)
}

func TestNewPermissionHandler_Deny(t *testing.T) {
	cfg := &config.Config{
		ToolPermissions: []config.ToolPermissionRule{
			{Title: "Bash", Action: config.ToolPermissionDeny},
		},
	}
	handler := NewPermissionHandler(cfg, nil)

	resp, err := handler(context.Background(), makeRequest("Bash", nil))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("deny"), resp.Outcome.Selected.OptionId)
}

func TestNewPermissionHandler_AskApproved(t *testing.T) {
	cfg := &config.Config{
		ToolPermissions: []config.ToolPermissionRule{
			{Title: "Read", Action: config.ToolPermissionAsk},
		},
	}
	ask := func(question string) (bool, error) {
		return true, nil
	}
	handler := NewPermissionHandler(cfg, ask)

	resp, err := handler(context.Background(), makeRequest("Read", nil))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("allow"), resp.Outcome.Selected.OptionId)
}

func TestNewPermissionHandler_AskDenied(t *testing.T) {
	cfg := &config.Config{
		ToolPermissions: []config.ToolPermissionRule{
			{Title: "Read", Action: config.ToolPermissionAsk},
		},
	}
	ask := func(question string) (bool, error) {
		return false, nil
	}
	handler := NewPermissionHandler(cfg, ask)

	resp, err := handler(context.Background(), makeRequest("Read", nil))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("deny"), resp.Outcome.Selected.OptionId)
}

func TestNewPermissionHandler_AskNoCallback(t *testing.T) {
	cfg := &config.Config{
		ToolPermissions: []config.ToolPermissionRule{
			{Title: "Read", Action: config.ToolPermissionAsk},
		},
	}
	// nil askPermission — should deny
	handler := NewPermissionHandler(cfg, nil)

	resp, err := handler(context.Background(), makeRequest("Read", nil))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("deny"), resp.Outcome.Selected.OptionId)
}

func TestNewPermissionHandler_NoMatch_AutoApproves(t *testing.T) {
	cfg := &config.Config{
		ToolPermissions: []config.ToolPermissionRule{
			{Title: "Bash", Action: config.ToolPermissionDeny},
		},
	}
	handler := NewPermissionHandler(cfg, nil)

	resp, err := handler(context.Background(), makeRequest("Read", nil))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("allow"), resp.Outcome.Selected.OptionId)
}

func TestNewPermissionHandler_InputPatternMatch(t *testing.T) {
	cfg := &config.Config{
		ToolPermissions: []config.ToolPermissionRule{
			{
				Title:  "Read",
				Action: config.ToolPermissionDeny,
				Input:  map[string]string{"file_path": "*.netrc"},
			},
		},
	}
	handler := NewPermissionHandler(cfg, nil)

	// Matches
	resp, err := handler(context.Background(), makeRequest("Read", map[string]interface{}{
		"file_path": "/home/user/.netrc",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("deny"), resp.Outcome.Selected.OptionId)

	// Doesn't match
	resp, err = handler(context.Background(), makeRequest("Read", map[string]interface{}{
		"file_path": "/home/user/.bashrc",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("allow"), resp.Outcome.Selected.OptionId)
}

func TestExtractInput(t *testing.T) {
	result := extractInput(map[string]interface{}{
		"file_path": "/home/user/.netrc",
		"content":   "secret",
		"count":     42, // non-string — should be skipped
	})
	require.Equal(t, map[string]string{
		"file_path": "/home/user/.netrc",
		"content":   "secret",
	}, result)
}

func TestExtractInput_Nil(t *testing.T) {
	result := extractInput(nil)
	require.Empty(t, result)
}

func TestDenyPermission_SelectsReject(t *testing.T) {
	resp, err := denyPermission([]acp.PermissionOption{approveOption(), rejectOption()})
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("deny"), resp.Outcome.Selected.OptionId)
}

func TestDenyPermission_NoRejectOption_Cancels(t *testing.T) {
	resp, err := denyPermission([]acp.PermissionOption{approveOption()})
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Cancelled)
}
