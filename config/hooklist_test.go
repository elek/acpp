package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseHookList_Empty(t *testing.T) {
	for _, in := range []string{"", "   ", ",", " , ,"} {
		got, err := ParseHookList(in)
		require.NoError(t, err, "input %q", in)
		require.Empty(t, got, "input %q", in)
	}
}

func TestParseHookList_BareTypes(t *testing.T) {
	got, err := ParseHookList("commit,worktree")
	require.NoError(t, err)
	require.Equal(t, []HookConfig{
		{Type: "commit", Params: map[string]string{}},
		{Type: "worktree", Params: map[string]string{}},
	}, got)
}

func TestParseHookList_TrimsWhitespace(t *testing.T) {
	got, err := ParseHookList("  commit , worktree  ")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "commit", got[0].Type)
	require.Equal(t, "worktree", got[1].Type)
}

func TestParseHookList_SingleParam(t *testing.T) {
	got, err := ParseHookList("worktree:location=/tmp/wt")
	require.NoError(t, err)
	require.Equal(t, []HookConfig{
		{Type: "worktree", Params: map[string]string{"location": "/tmp/wt"}},
	}, got)
}

func TestParseHookList_MultipleParams(t *testing.T) {
	got, err := ParseHookList("worktree:location=/tmp/wt;mode=fast")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"location": "/tmp/wt", "mode": "fast"}, got[0].Params)
}

func TestParseHookList_ParamValueMayContainEquals(t *testing.T) {
	// Only the first "=" separates key from value, so values carrying "=" survive.
	got, err := ParseHookList("qdrant:url=http://x/?a=b")
	require.NoError(t, err)
	require.Equal(t, "http://x/?a=b", got[0].Params["url"])
}

func TestParseHookList_MixedBareAndParameterised(t *testing.T) {
	got, err := ParseHookList("commit,worktree:location=/tmp/wt")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Empty(t, got[0].Params)
	require.Equal(t, "/tmp/wt", got[1].Params["location"])
}

func TestParseHookList_MissingTypeIsAnError(t *testing.T) {
	_, err := ParseHookList(":location=/tmp")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing hook type")
}

func TestParseHookList_MalformedParamIsAnError(t *testing.T) {
	_, err := ParseHookList("worktree:location")
	require.Error(t, err)
	require.Contains(t, err.Error(), "location")
}

func TestParseHookList_EmptyParamKeyIsAnError(t *testing.T) {
	_, err := ParseHookList("worktree:=/tmp")
	require.Error(t, err)
}

func TestFormatHookList_RoundTrips(t *testing.T) {
	for _, in := range []string{
		"commit",
		"commit,worktree",
		"worktree:location=/tmp/wt",
		"commit,worktree:location=/tmp/wt",
	} {
		parsed, err := ParseHookList(in)
		require.NoError(t, err, "input %q", in)
		require.Equal(t, in, FormatHookList(parsed), "input %q", in)
	}
}

func TestFormatHookList_MultipleParamsAreSorted(t *testing.T) {
	// Map iteration order is random, so formatting must sort to be stable.
	cfgs := []HookConfig{{Type: "worktree", Params: map[string]string{"z": "1", "a": "2"}}}
	require.Equal(t, "worktree:a=2;z=1", FormatHookList(cfgs))
}
