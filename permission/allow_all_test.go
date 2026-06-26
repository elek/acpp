package permission

import (
	"testing"

	"github.com/elek/acpp/acp"
	"github.com/stretchr/testify/require"
)

func allowOnce() acp.PermissionOption {
	return acp.PermissionOption{OptionId: "allow", Kind: acp.PermissionOptionKindAllowOnce}
}

func rejectOnce() acp.PermissionOption {
	return acp.PermissionOption{OptionId: "deny", Kind: acp.PermissionOptionKindRejectOnce}
}

func TestDecide_SelectsAllowOnce(t *testing.T) {
	resp := decide([]acp.PermissionOption{rejectOnce(), allowOnce()})
	require.NotNil(t, resp)
	require.NotNil(t, resp.Outcome.Selected)
	require.Equal(t, acp.PermissionOptionId("allow"), resp.Outcome.Selected.OptionId)
}

func TestDecide_AllowAlwaysIsNotAllowOnce(t *testing.T) {
	// allow_always does not contain "once", so it is not auto-selected.
	resp := decide([]acp.PermissionOption{
		{OptionId: "always", Kind: acp.PermissionOptionKindAllowAlways},
	})
	require.Nil(t, resp)
}

func TestDecide_NoAllowOption_ReturnsNil(t *testing.T) {
	resp := decide([]acp.PermissionOption{rejectOnce()})
	require.Nil(t, resp)
}

func TestDecide_NoOptions_Cancels(t *testing.T) {
	resp := decide(nil)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Outcome.Cancelled)
}
