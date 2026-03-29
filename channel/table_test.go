package channel

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMarkdownTable_Simple(t *testing.T) {
	lines := []string{
		"| Name | Score |",
		"|------|-------|",
		"| Alice | 95 |",
		"| Bob | 87 |",
	}

	table := ParseMarkdownTable(lines)
	require.NotNil(t, table)

	assert.Equal(t, []string{"Name", "Score"}, table.Headers)
	require.Len(t, table.Rows, 2)
	assert.Equal(t, []string{"Alice", "95"}, table.Rows[0])
	assert.Equal(t, []string{"Bob", "87"}, table.Rows[1])
}

func TestParseMarkdownTable_ThreeColumns(t *testing.T) {
	lines := []string{
		"| Item | Quantity | Price |",
		"|------|----------|-------|",
		"| Apple | 5 | $1.00 |",
		"| Banana | 3 | $0.50 |",
	}

	table := ParseMarkdownTable(lines)
	require.NotNil(t, table)

	assert.Equal(t, []string{"Item", "Quantity", "Price"}, table.Headers)
	require.Len(t, table.Rows, 2)
	assert.Equal(t, []string{"Apple", "5", "$1.00"}, table.Rows[0])
}

func TestParseMarkdownTable_NoSeparator(t *testing.T) {
	lines := []string{
		"| Name | Score |",
		"| Alice | 95 |",
	}

	table := ParseMarkdownTable(lines)
	require.NotNil(t, table)

	assert.Equal(t, []string{"Name", "Score"}, table.Headers)
	require.Len(t, table.Rows, 1)
}

func TestParseMarkdownTable_TooFewLines(t *testing.T) {
	lines := []string{
		"| Name |",
	}

	table := ParseMarkdownTable(lines)
	assert.Nil(t, table)
}

func TestParseMarkdownTable_EmptyLines(t *testing.T) {
	lines := []string{}

	table := ParseMarkdownTable(lines)
	assert.Nil(t, table)
}

func TestParseTableRow(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"| A | B |", []string{"A", "B"}},
		{"|A|B|", []string{"A", "B"}},
		{"| A | B | C |", []string{"A", "B", "C"}},
		{"  | A | B |  ", []string{"A", "B"}},
		{"not a row", nil},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseTableRow(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsSeparatorRow(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"|---|---|", true},
		{"| --- | --- |", true},
		{"|:---|---:|", true},
		{"|:---:|:---:|", true},
		{"| A | B |", false},
		{"|123|456|", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isSeparatorRow(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTable_CanRenderAsEmbed_Valid(t *testing.T) {
	table := &Table{
		Headers: []string{"Name", "Score"},
		Rows: [][]string{
			{"Alice", "95"},
			{"Bob", "87"},
		},
	}

	assert.True(t, table.CanRenderAsEmbed())
}

func TestTable_CanRenderAsEmbed_TooManyColumns(t *testing.T) {
	table := &Table{
		Headers: []string{"A", "B", "C", "D"}, // 4 columns > 3 max
		Rows: [][]string{
			{"1", "2", "3", "4"},
		},
	}

	assert.False(t, table.CanRenderAsEmbed())
}

func TestTable_CanRenderAsEmbed_TooManyRows(t *testing.T) {
	table := &Table{
		Headers: []string{"A", "B", "C"},
		Rows:    make([][]string, 10), // 10 rows * 3 cols + 3 header = 33 fields > 25 max
	}

	for i := range table.Rows {
		table.Rows[i] = []string{"x", "y", "z"}
	}

	assert.False(t, table.CanRenderAsEmbed())
}

func TestTable_CanRenderAsEmbed_FieldNameTooLong(t *testing.T) {
	longName := make([]byte, 300)
	for i := range longName {
		longName[i] = 'x'
	}

	table := &Table{
		Headers: []string{string(longName)}, // > 256 chars
		Rows: [][]string{
			{"value"},
		},
	}

	assert.False(t, table.CanRenderAsEmbed())
}

func TestTable_CanRenderAsEmbed_NilTable(t *testing.T) {
	var table *Table
	assert.False(t, table.CanRenderAsEmbed())
}

func TestTable_ToEmbed_TwoColumns(t *testing.T) {
	table := &Table{
		Headers: []string{"Name", "Score"},
		Rows: [][]string{
			{"Alice", "95"},
			{"Bob", "87"},
		},
	}

	embed := table.ToEmbed()
	require.NotNil(t, embed)

	// 2 columns + 1 padding = 3 fields per row
	// Header row (3) + 2 data rows (6) = 9 fields
	assert.Len(t, embed.Fields, 9)

	// Check headers
	assert.Equal(t, "Name", embed.Fields[0].Name)
	assert.Equal(t, "Score", embed.Fields[1].Name)
	assert.True(t, embed.Fields[0].Inline)

	// Check first data row values (fields 3, 4, 5)
	assert.Equal(t, "Alice", embed.Fields[3].Value)
	assert.Equal(t, "95", embed.Fields[4].Value)
}

func TestTable_ToEmbed_ThreeColumns(t *testing.T) {
	table := &Table{
		Headers: []string{"A", "B", "C"},
		Rows: [][]string{
			{"1", "2", "3"},
		},
	}

	embed := table.ToEmbed()
	require.NotNil(t, embed)

	// 3 columns, no padding needed
	// Header row (3) + 1 data row (3) = 6 fields
	assert.Len(t, embed.Fields, 6)
}

func TestTable_ToEmbed_Nil(t *testing.T) {
	var table *Table
	embed := table.ToEmbed()
	assert.Nil(t, embed)
}

func TestTable_ToCodeBlock(t *testing.T) {
	table := &Table{
		Headers: []string{"Name", "Score"},
		Rows: [][]string{
			{"Alice", "95"},
			{"Bob", "87"},
		},
	}

	result := table.ToCodeBlock()

	expected := "```\n| Name | Score |\n| --- | --- |\n| Alice | 95 |\n| Bob | 87 |\n```"
	assert.Equal(t, expected, result)
}

func TestTable_ToCodeBlock_Nil(t *testing.T) {
	var table *Table
	result := table.ToCodeBlock()
	assert.Equal(t, "", result)
}

func TestTableRenderer_Render_AsEmbed(t *testing.T) {
	table := &Table{
		Headers: []string{"Name", "Score"},
		Rows: [][]string{
			{"Alice", "95"},
		},
	}

	var embedCalled bool
	renderer := &TableRenderer{
		SendEmbed: func(embed *discordgo.MessageEmbed) error {
			embedCalled = true
			return nil
		},
		SendText: func(text string) error {
			t.Error("SendText should not be called")
			return nil
		},
	}

	err := renderer.Render(table)
	require.NoError(t, err)
	assert.True(t, embedCalled)
}

func TestTableRenderer_Render_AsCodeBlock(t *testing.T) {
	// Table with 4 columns - too many for embed
	table := &Table{
		Headers: []string{"A", "B", "C", "D"},
		Rows: [][]string{
			{"1", "2", "3", "4"},
		},
	}

	var textCalled bool
	renderer := &TableRenderer{
		SendEmbed: func(embed *discordgo.MessageEmbed) error {
			t.Error("SendEmbed should not be called")
			return nil
		},
		SendText: func(text string) error {
			textCalled = true
			assert.Contains(t, text, "```")
			return nil
		},
	}

	err := renderer.Render(table)
	require.NoError(t, err)
	assert.True(t, textCalled)
}

// Test integration with BlockBuffer

func TestBlockBuffer_TableWithCallback(t *testing.T) {
	var textOutput []string
	var tableLines []string

	cb := NewBlockBuffer(func(line string, update bool) error {
		textOutput = append(textOutput, line)
		return nil
	})

	cb.SetTableOutput(func(lines []string) error {
		tableLines = lines
		return nil
	})

	cb.Write("Before table\n")
	cb.Write("| A | B |\n")
	cb.Write("|---|---|\n")
	cb.Write("| 1 | 2 |\n")
	cb.Write("After table\n")

	assert.Equal(t, []string{"Before table", "After table"}, textOutput)
	assert.Equal(t, []string{"| A | B |", "|---|---|", "| 1 | 2 |"}, tableLines)
}

func TestBlockBuffer_TableWithoutCallback(t *testing.T) {
	var output []string

	cb := NewBlockBuffer(func(line string, update bool) error {
		output = append(output, line)
		return nil
	})
	// No SetTableOutput called - should fall back to code block

	cb.Write("| A | B |\n")
	cb.Write("|---|---|\n")
	cb.Write("| 1 | 2 |\n")
	cb.Write("Done\n")

	require.Len(t, output, 2)
	assert.Contains(t, output[0], "```")
	assert.Contains(t, output[0], "| A | B |")
	assert.Equal(t, "Done", output[1])
}
