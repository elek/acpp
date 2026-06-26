package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWildcardMatch(t *testing.T) {
	tests := []struct {
		pattern string
		s       string
		match   bool
	}{
		{"read", "read", true},
		{"read", "write", false},
		{"*", "anything", true},
		{"*.netrc", "/home/user/.netrc", true},
		{"*.netrc", "/home/user/.bashrc", false},
		{"*rm -rf*", "sudo rm -rf /", true},
		{"*rm -rf*", "echo hello", false},
		{"bash*", "bash", true},
		{"bash*", "bashrc", true},
		{"bash*", "fish", false},
		{"*secret*file*", "/path/to/secret/my/file.txt", true},
		{"*secret*file*", "/path/to/public/my/file.txt", false},
		{"exact", "exact", true},
		{"exact", "notexact", false},
		{"", "", true},
		{"*", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.s, func(t *testing.T) {
			require.Equal(t, tt.match, wildcardMatch(tt.pattern, tt.s))
		})
	}
}

func TestMatchToolPermission_TitleOnly(t *testing.T) {
	cfg := &Config{
		ToolPermissions: ToolPermissions{Rules: []ToolPermissionRule{
			{Title: "Bash", Action: ToolPermissionDeny},
			{Title: "Read", Action: ToolPermissionAsk},
		}},
	}

	require.Equal(t, ToolPermissionDeny, cfg.MatchToolPermission("Bash", nil))
	require.Equal(t, ToolPermissionAsk, cfg.MatchToolPermission("Read", nil))
	require.Equal(t, ToolPermissionAction(""), cfg.MatchToolPermission("Write", nil))
}

func TestMatchToolPermission_CaseInsensitive(t *testing.T) {
	cfg := &Config{
		ToolPermissions: ToolPermissions{Rules: []ToolPermissionRule{
			{Title: "bash", Action: ToolPermissionDeny},
		}},
	}

	require.Equal(t, ToolPermissionDeny, cfg.MatchToolPermission("Bash", nil))
	require.Equal(t, ToolPermissionDeny, cfg.MatchToolPermission("BASH", nil))
	require.Equal(t, ToolPermissionDeny, cfg.MatchToolPermission("bash", nil))
}

func TestMatchToolPermission_InputOnly(t *testing.T) {
	cfg := &Config{
		ToolPermissions: ToolPermissions{Rules: []ToolPermissionRule{
			{
				Action: ToolPermissionDeny,
				Input:  map[string]string{"command": "*rm -rf*"},
			},
		}},
	}

	require.Equal(t, ToolPermissionDeny, cfg.MatchToolPermission("Bash", map[string]string{"command": "rm -rf /"}))
	require.Equal(t, ToolPermissionAction(""), cfg.MatchToolPermission("Bash", map[string]string{"command": "ls -la"}))
}

func TestMatchToolPermission_TitleAndInput(t *testing.T) {
	cfg := &Config{
		ToolPermissions: ToolPermissions{Rules: []ToolPermissionRule{
			{
				Title:  "Read",
				Action: ToolPermissionAsk,
				Input:  map[string]string{"file_path": "*.netrc"},
			},
		}},
	}

	// Both title AND input must match
	require.Equal(t, ToolPermissionAsk, cfg.MatchToolPermission("Read", map[string]string{"file_path": "/home/user/.netrc"}))
	// Title matches but input doesn't
	require.Equal(t, ToolPermissionAction(""), cfg.MatchToolPermission("Read", map[string]string{"file_path": "/home/user/.bashrc"}))
	// Input matches but title doesn't
	require.Equal(t, ToolPermissionAction(""), cfg.MatchToolPermission("Write", map[string]string{"file_path": "/home/user/.netrc"}))
}

func TestMatchToolPermission_FirstRuleWins(t *testing.T) {
	cfg := &Config{
		ToolPermissions: ToolPermissions{Rules: []ToolPermissionRule{
			{Title: "Bash", Action: ToolPermissionDeny},
			{Title: "Bash", Action: ToolPermissionAsk},
		}},
	}

	// First rule wins
	require.Equal(t, ToolPermissionDeny, cfg.MatchToolPermission("Bash", nil))
}

func TestMatchToolPermission_MultipleInputParams(t *testing.T) {
	cfg := &Config{
		ToolPermissions: ToolPermissions{Rules: []ToolPermissionRule{
			{
				Title:  "Edit",
				Action: ToolPermissionDeny,
				Input: map[string]string{
					"file_path":  "*/etc/*",
					"new_string": "*password*",
				},
			},
		}},
	}

	// Both input params match
	require.Equal(t, ToolPermissionDeny, cfg.MatchToolPermission("Edit", map[string]string{
		"file_path":  "/etc/shadow",
		"new_string": "newpassword123",
	}))
	// Only one input param matches
	require.Equal(t, ToolPermissionAction(""), cfg.MatchToolPermission("Edit", map[string]string{
		"file_path":  "/etc/shadow",
		"new_string": "harmless",
	}))
}

func TestMatchToolPermission_EmptyRuleSkipped(t *testing.T) {
	cfg := &Config{
		ToolPermissions: ToolPermissions{Rules: []ToolPermissionRule{
			{Action: ToolPermissionDeny}, // no title or input — should not match anything
		}},
	}

	require.Equal(t, ToolPermissionAction(""), cfg.MatchToolPermission("Bash", map[string]string{"command": "ls"}))
}

func TestMatchToolPermission_NoRules(t *testing.T) {
	cfg := &Config{}
	require.Equal(t, ToolPermissionAction(""), cfg.MatchToolPermission("Bash", nil))
}
