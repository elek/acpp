package helpers

import (
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Discord embed limits
const (
	maxEmbedFields        = 25
	maxFieldNameChars     = 256
	maxFieldValueChars    = 1024
	maxEmbedTotalChars    = 6000
	maxEmbedTitleChars    = 256
	maxInlineFieldsPerRow = 3
)

// Table represents a parsed markdown table.
type Table struct {
	Headers []string
	Rows    [][]string
}

// ParseMarkdownTable parses a markdown table from lines starting with |.
// Returns nil if the input is not a valid table.
func ParseMarkdownTable(lines []string) *Table {
	if len(lines) < 2 {
		return nil
	}

	table := &Table{}

	for i, line := range lines {
		cells := parseTableRow(line)
		if cells == nil {
			continue
		}

		if i == 0 {
			// First row is headers
			table.Headers = cells
		} else if i == 1 && isSeparatorRow(line) {
			// Skip separator row (|---|---|)
			continue
		} else {
			// Data rows
			table.Rows = append(table.Rows, cells)
		}
	}

	if len(table.Headers) == 0 {
		return nil
	}

	return table
}

// parseTableRow extracts cells from a table row like "| A | B | C |"
func parseTableRow(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return nil
	}

	// Remove leading and trailing |
	line = strings.Trim(line, "|")

	// Split by |
	parts := strings.Split(line, "|")

	var cells []string
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}

	return cells
}

// isSeparatorRow checks if a row is a separator like |---|---|
func isSeparatorRow(line string) bool {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		// Separator cells contain only dashes and colons (for alignment)
		for _, ch := range part {
			if ch != '-' && ch != ':' {
				return false
			}
		}
	}
	return true
}

// CanRenderAsEmbed checks if the table can be rendered as a Discord embed
// within the embed limits.
func (t *Table) CanRenderAsEmbed() bool {
	if t == nil || len(t.Headers) == 0 {
		return false
	}

	// Check column count - embeds work best with 2-3 columns
	// With inline fields, we can display up to 3 columns per row
	if len(t.Headers) > maxInlineFieldsPerRow {
		return false
	}

	// Check total field count (headers + all data cells)
	// Each row needs len(Headers) fields
	totalFields := len(t.Headers) * (1 + len(t.Rows)) // header row + data rows
	if totalFields > maxEmbedFields {
		return false
	}

	// Check character limits
	totalChars := 0
	for _, h := range t.Headers {
		if len(h) > maxFieldNameChars {
			return false
		}
		totalChars += len(h)
	}

	for _, row := range t.Rows {
		for _, cell := range row {
			if len(cell) > maxFieldValueChars {
				return false
			}
			totalChars += len(cell)
		}
	}

	if totalChars > maxEmbedTotalChars {
		return false
	}

	return true
}

// ToEmbed converts the table to a Discord embed with inline fields.
func (t *Table) ToEmbed() *discordgo.MessageEmbed {
	if t == nil {
		return nil
	}

	embed := &discordgo.MessageEmbed{
		Fields: make([]*discordgo.MessageEmbedField, 0),
	}

	numCols := len(t.Headers)

	// Add header row as the first set of fields
	for _, header := range t.Headers {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   header,
			Value:  "━━━━━", // Visual separator under header
			Inline: true,
		})
	}

	// Add padding fields if needed to complete the row (for 2-column tables)
	if numCols < maxInlineFieldsPerRow {
		for i := numCols; i < maxInlineFieldsPerRow; i++ {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "\u200b", // Zero-width space
				Value:  "\u200b",
				Inline: true,
			})
		}
	}

	// Add data rows
	for _, row := range t.Rows {
		// Add cells for this row
		for colIdx := 0; colIdx < numCols; colIdx++ {
			value := "\u200b" // Default to zero-width space for empty cells
			if colIdx < len(row) && row[colIdx] != "" {
				value = row[colIdx]
			}

			// Use header as field name for consistent column identification
			// But for data rows, use zero-width space as name to avoid repetition
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "\u200b",
				Value:  value,
				Inline: true,
			})
		}

		// Add padding fields if needed to complete the row
		if numCols < maxInlineFieldsPerRow {
			for i := numCols; i < maxInlineFieldsPerRow; i++ {
				embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
					Name:   "\u200b",
					Value:  "\u200b",
					Inline: true,
				})
			}
		}
	}

	return embed
}

// ToCodeBlock converts the table to a code block string for fallback rendering.
func (t *Table) ToCodeBlock() string {
	if t == nil {
		return ""
	}

	var lines []string

	// Reconstruct the original table format
	if len(t.Headers) > 0 {
		lines = append(lines, "| "+strings.Join(t.Headers, " | ")+" |")

		// Add separator
		var seps []string
		for range t.Headers {
			seps = append(seps, "---")
		}
		lines = append(lines, "| "+strings.Join(seps, " | ")+" |")
	}

	for _, row := range t.Rows {
		lines = append(lines, "| "+strings.Join(row, " | ")+" |")
	}

	return "```\n" + strings.Join(lines, "\n") + "\n```"
}

// TableRenderer handles rendering tables for Discord.
type TableRenderer struct {
	// SendEmbed is called when a table should be rendered as an embed
	SendEmbed func(embed *discordgo.MessageEmbed) error
	// SendText is called when a table should be rendered as a code block
	SendText func(text string) error
}

// Render renders a table, choosing embed or code block based on limits.
func (r *TableRenderer) Render(table *Table) error {
	if table == nil {
		return nil
	}

	if table.CanRenderAsEmbed() && r.SendEmbed != nil {
		return r.SendEmbed(table.ToEmbed())
	}

	// Fallback to code block
	if r.SendText != nil {
		return r.SendText(table.ToCodeBlock())
	}

	return nil
}
