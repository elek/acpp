package discord

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitMessage_Short(t *testing.T) {
	msg := "short message"
	chunks := splitMessage(msg)
	require.Len(t, chunks, 1)
	assert.Equal(t, msg, chunks[0])
}

func TestSplitMessage_ExactLimit(t *testing.T) {
	msg := strings.Repeat("a", discordMaxMessageLen)
	chunks := splitMessage(msg)
	require.Len(t, chunks, 1)
	assert.Equal(t, msg, chunks[0])
}

func TestSplitMessage_PlainTextSplit(t *testing.T) {
	// Build a message just over the limit with newlines
	line := strings.Repeat("x", 100) + "\n"
	msg := strings.Repeat(line, 25) // 25 * 101 = 2525 chars
	chunks := splitMessage(msg)
	require.Greater(t, len(chunks), 1)
	for _, chunk := range chunks {
		assert.LessOrEqual(t, len(chunk), discordMaxMessageLen)
	}
	// Recombining chunks should give back the original content (minus split points)
}

func TestSplitMessage_CodeBlockSplit(t *testing.T) {
	// Build a code block that exceeds 2000 chars
	var sb strings.Builder
	sb.WriteString("```\n")
	for i := 0; i < 30; i++ {
		sb.WriteString(strings.Repeat("x", 80) + "\n")
	}
	sb.WriteString("```")
	msg := sb.String()

	chunks := splitMessage(msg)
	require.Greater(t, len(chunks), 1)
	for _, chunk := range chunks {
		assert.LessOrEqual(t, len(chunk), discordMaxMessageLen)
	}

	// First chunk should start with ``` and end with ```
	assert.True(t, strings.HasPrefix(chunks[0], "```"))
	assert.True(t, strings.HasSuffix(chunks[0], "```"))

	// Second chunk should also be wrapped in fences
	assert.True(t, strings.HasPrefix(chunks[1], "```"))
}

func TestSplitMessage_CodeBlockPreservesContent(t *testing.T) {
	// Build a code block that needs splitting
	var lines []string
	lines = append(lines, "```")
	for i := 0; i < 30; i++ {
		lines = append(lines, strings.Repeat("x", 80))
	}
	lines = append(lines, "```")
	msg := strings.Join(lines, "\n")

	chunks := splitMessage(msg)

	// Extract content from all chunks (strip fences added by splitting)
	var allContent []string
	for _, chunk := range chunks {
		// Remove wrapping fences
		content := chunk
		content = strings.TrimPrefix(content, "```\n")
		content = strings.TrimSuffix(content, "\n```")
		allContent = append(allContent, content)
	}

	// Rejoined content should contain all original lines
	joined := strings.Join(allContent, "\n")
	for i := 0; i < 30; i++ {
		assert.Contains(t, joined, strings.Repeat("x", 80))
	}
}

func TestIsInsideCodeBlock(t *testing.T) {
	assert.True(t, isInsideCodeBlock("```\nsome code"))
	assert.False(t, isInsideCodeBlock("```\ncode\n```"))
	assert.True(t, isInsideCodeBlock("text\n```\ncode"))
	assert.False(t, isInsideCodeBlock("no code blocks here"))
}

func TestStripCodeBlockLanguage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"plain code block", "```\ncode\n```", "```\ncode\n```"},
		{"yaml language", "```yaml\nkey: value\n```", "```\nkey: value\n```"},
		{"go language", "```go\nfmt.Println()\n```", "```\nfmt.Println()\n```"},
		{"not a code block", "hello world", "hello world"},
		{"backticks mid-line", "use ``` for blocks", "use ``` for blocks"},
		{"no newline", "```go", "```go"},
		{"empty block", "```\n```", "```\n```"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, stripCodeBlockLanguage(tt.input))
		})
	}
}
