package discord

import "testing"

func TestUnwrapBacktickCommand(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"backticked command", "`/clear`", "/clear"},
		{"backticked command with args", "`/cancel now`", "/cancel now"},
		{"surrounding whitespace", "  `/clear`  ", "/clear"},
		{"plain command untouched", "/clear", "/clear"},
		{"non-command stays wrapped", "`some code`", "`some code`"},
		{"multiline untouched", "`/clear`\nmore", "`/clear`\nmore"},
		{"inner newline untouched", "`/cle\nar`", "`/cle\nar`"},
		{"only opening backtick", "`/clear", "`/clear"},
		{"only closing backtick", "/clear`", "/clear`"},
		{"empty backticks", "``", "``"},
		{"plain message untouched", "hello world", "hello world"},
		{"code fence untouched", "```/clear```", "```/clear```"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unwrapBacktickCommand(tc.in); got != tc.want {
				t.Errorf("unwrapBacktickCommand(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
