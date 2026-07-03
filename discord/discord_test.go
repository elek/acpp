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

func TestStripCodeBlockLanguage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"block at start", "```yaml\nfoo: bar\n```", "```\nfoo: bar\n```"},
		{"no language untouched", "```\nfoo: bar\n```", "```\nfoo: bar\n```"},
		{"text before block", "config:\n```yaml\nfoo: bar\n```", "config:\n```\nfoo: bar\n```"},
		{"multiple blocks", "```yaml\na\n```\ntext\n```json\n{}\n```", "```\na\n```\ntext\n```\n{}\n```"},
		{"plain text untouched", "hello world", "hello world"},
		{"backticks inside body untouched", "```go\nx := `raw`\n```", "```\nx := `raw`\n```"},
		{"four backtick fence", "````md\ncontent\n````", "````\ncontent\n````"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripCodeBlockLanguage(tc.in); got != tc.want {
				t.Errorf("stripCodeBlockLanguage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
