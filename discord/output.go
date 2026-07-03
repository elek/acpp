package discord

import (
	"fmt"
	"sort"
	"strings"

	"github.com/elek/acpp/acp"
)

// stopReasonEmojis maps a turn's stop reason to a display emoji.
var stopReasonEmojis = map[acp.StopReason]string{
	acp.StopReasonEndTurn:         "🏁",
	acp.StopReasonMaxTokens:       "📏",
	acp.StopReasonMaxTurnRequests: "🔁",
	acp.StopReasonRefusal:         "🚫",
	acp.StopReasonCancelled:       "🛑",
}

// toolEmojis maps tool kinds to their display emojis.
var toolEmojis = map[string]string{
	"read":    "📖",
	"search":  "🔍",
	"think":   "🤖",
	"execute": "⚡",
	"edit":    "✏️",
	"fetch":   "🌐",
}

// statusEmojis maps tool-call status to their display emojis.
var statusEmojis = map[string]string{
	"pending":     "⏳",
	"in-progress": "🔄",
	"completed":   "✅",
	"failed":      "❌",
}

// planStatusEmojis maps plan entry status to their display emojis.
var planStatusEmojis = map[string]string{
	"pending":     "⏳",
	"in_progress": "🔄",
	"completed":   "✅",
}

const discordMaxMessageLen = 2000

// splitMessage splits a message into chunks that fit within Discord's 2000-char
// limit. It is code-block-aware: if a chunk ends inside a code block, it closes
// the fence and reopens it in the next chunk.
func splitMessage(s string) []string {
	if len(s) <= discordMaxMessageLen {
		return []string{s}
	}

	var chunks []string
	inCodeBlock := false

	for len(s) > 0 {
		limit := discordMaxMessageLen
		// Reserve space for closing fence if we might need it.
		if inCodeBlock {
			limit -= 4 // "\n```"
		}

		if len(s) <= limit {
			chunks = append(chunks, s)
			break
		}

		// Find a newline to split on, searching backwards from the limit.
		// Reserve extra space in case we need to add a closing fence.
		searchLimit := limit
		if !inCodeBlock {
			searchLimit = limit - 4
		}
		if searchLimit > len(s) {
			searchLimit = len(s)
		}
		cut := strings.LastIndex(s[:searchLimit], "\n")
		if cut <= 0 {
			cut = searchLimit
		}

		chunk := s[:cut]

		// Determine if this chunk ends inside a code block by counting fences
		// relative to the state we entered with.
		endsInCodeBlock := inCodeBlock != (strings.Count(chunk, "```")%2 != 0)

		if endsInCodeBlock {
			chunk += "\n```"
		}

		chunks = append(chunks, chunk)

		// Advance past the cut point.
		s = s[cut:]
		if len(s) > 0 && s[0] == '\n' {
			s = s[1:]
		}

		// Reopen code block in next chunk if needed.
		if endsInCodeBlock {
			s = "```\n" + s
		}

		inCodeBlock = false // we always close/reopen, so next iteration starts clean
	}

	return chunks
}

// stripCodeBlockLanguage removes the language identifier from every opening
// markdown code-block fence in s (e.g., "```yaml\n..." becomes "```\n...").
// Discord doesn't render language-specific syntax highlighting, and some
// clients fail to detect the block at all when an unknown identifier follows
// the fence, so the identifier is noise. Fences may appear anywhere in s (not
// just at the start), and s may contain multiple blocks.
func stripCodeBlockLanguage(s string) string {
	lines := strings.Split(s, "\n")
	inCodeBlock := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "```") {
			continue
		}
		if inCodeBlock {
			// Closing fence - leave as-is.
			inCodeBlock = false
			continue
		}
		// Opening fence - drop anything after the run of backticks.
		inCodeBlock = true
		n := 0
		for n < len(line) && line[n] == '`' {
			n++
		}
		lines[i] = line[:n]
	}
	return strings.Join(lines, "\n")
}

// formatToolUsage renders a tool-call into a Discord message string.
func formatToolUsage(toolUsage ToolUsage) string {
	var lines []string

	// First line: emoji + type + title + status.
	var firstLine strings.Builder

	// Special handling for the Skill tool: display "🎯 skill: skill_name".
	if skillName, ok := toolUsage.Input["skill"]; ok {
		firstLine.WriteString("🎯 skill: " + skillName)
	} else if strings.HasPrefix(toolUsage.Title, "mcp__") {
		// MCP tool call — display parts separated by spaces instead of __.
		firstLine.WriteString("🔌 " + strings.ReplaceAll(toolUsage.Title, "__", " "))
	} else {
		if emoji, ok := toolEmojis[toolUsage.ToolKind]; ok {
			firstLine.WriteString(emoji + " ")
		} else {
			firstLine.WriteString(toolUsage.ToolKind + " ")
		}
		if toolUsage.Title != "" {
			firstLine.WriteString(" " + toolUsage.Title)
		}
	}

	// Status with emoji.
	if toolUsage.Status != "" {
		statusEmoji := statusEmojis[toolUsage.Status]
		if statusEmoji != "" {
			firstLine.WriteString(" " + statusEmoji)
		} else {
			firstLine.WriteString(" [" + toolUsage.Status + "]")
		}
	}

	if firstLine.Len() > 0 {
		lines = append(lines, firstLine.String())
	}

	// Second line: input parameters (only when title is NOT set).
	if len(toolUsage.Input) > 0 && toolUsage.Title == "" {
		keys := make([]string, 0, len(toolUsage.Input))
		for key := range toolUsage.Input {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		var params []string
		for _, key := range keys {
			value := toolUsage.Input[key]
			if len(value) > 80 {
				value = value[:77] + "..."
			}
			params = append(params, fmt.Sprintf("`%s`: %s", key, value))
		}
		if len(params) > 0 {
			lines = append(lines, strings.Join(params, " | "))
		}
	}

	return strings.Join(lines, "\n")
}

// formatPlanUpdate renders a plan into a Discord code block with a progress bar.
func formatPlanUpdate(plan PlanUpdate) string {
	var lines []string

	completed := 0
	total := len(plan.Entries)
	for _, entry := range plan.Entries {
		if entry.Status == "completed" {
			completed++
		}
	}

	header := fmt.Sprintf("📋 Plan (%d/%d completed) %s", completed, total, buildProgressBar(completed, total, 10))
	lines = append(lines, header, "")

	for _, entry := range plan.Entries {
		emoji := planStatusEmojis[entry.Status]
		if emoji == "" {
			emoji = "⏳"
		}
		lines = append(lines, fmt.Sprintf("%s %s", emoji, entry.Content))
	}

	return "```\n" + strings.Join(lines, "\n") + "\n```"
}

func buildProgressBar(completed, total, width int) string {
	if total == 0 {
		return strings.Repeat("░", width)
	}
	filled := (completed * width) / total
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// formatPromptResponse renders a completed turn's stop reason and token usage as
// a compact one-line Discord message decorated with emojis. Returns "" when
// there is nothing meaningful to show.
func formatPromptResponse(resp acp.PromptResponse) string {
	var parts []string

	if resp.StopReason != "" {
		emoji := stopReasonEmojis[resp.StopReason]
		if emoji == "" {
			emoji = "🏁"
		}
		parts = append(parts, fmt.Sprintf("%s %s", emoji, resp.StopReason))
	}

	if u := resp.Usage; u != nil {
		var tokens []string
		add := func(emoji string, n int) {
			if n > 0 {
				tokens = append(tokens, fmt.Sprintf("%s %s", emoji, humanizeTokens(n)))
			}
		}
		add("📥", u.InputTokens)
		add("📤", u.OutputTokens)
		if u.CachedReadTokens != nil {
			add("♻️", *u.CachedReadTokens)
		}
		if u.ThoughtTokens != nil {
			add("💭", *u.ThoughtTokens)
		}
		add("🧮", u.TotalTokens)
		if len(tokens) > 0 {
			parts = append(parts, strings.Join(tokens, " "))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// humanizeTokens formats a token count compactly (e.g. 1234 -> "1.2k").
func humanizeTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
