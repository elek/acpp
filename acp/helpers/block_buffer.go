package helpers

import (
	"strings"
	"sync"
	"time"
)

// BlockBuffer wraps a LineBuffer and buffers code blocks and tables.
// When a line starts with ``` (markdown code block), all lines are buffered
// until the closing ```. The entire code block is then output as a single
// call with newlines included.
// When a line starts with | (markdown table), all consecutive lines starting
// with | are buffered and output via the tableOutput callback if set, or as
// a code block fallback.
type BlockBuffer struct {
	lineBuffer  *LineBuffer
	output      func(line string, update bool) error
	tableOutput func(lines []string) error // Optional callback for table rendering

	mu            sync.Mutex
	inCodeBlock   bool
	inTable       bool
	codeBuffer    strings.Builder
	tableLines    []string // Buffered table lines for tableOutput callback
	codeBlockSent bool     // Whether we've already sent an update for current block
}

// NewBlockBuffer creates a new BlockBuffer with the given output function.
func NewBlockBuffer(output func(line string, update bool) error) *BlockBuffer {
	cb := &BlockBuffer{
		output: output,
	}
	cb.lineBuffer = NewLineBuffer(cb.handleLine)
	return cb
}

// SetTableOutput sets an optional callback for table rendering.
// When set, tables will be passed to this callback as a slice of lines.
// If not set, tables are rendered as code blocks.
func (b *BlockBuffer) SetTableOutput(fn func(lines []string) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tableOutput = fn
}

// SetFlushDelay sets the delay before partial lines are flushed.
// This is primarily useful for testing.
func (b *BlockBuffer) SetFlushDelay(d time.Duration) {
	b.lineBuffer.SetFlushDelay(d)
}

// handleLine processes a complete line from the LineBuffer.
func (b *BlockBuffer) handleLine(line string, update bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	isCodeFence := strings.HasPrefix(line, "```")
	isTableRow := strings.HasPrefix(line, "|")

	if b.inCodeBlock {
		// We're inside a code block
		if update {
			// This is an update to the previous partial line
			// Remove the last line from the buffer and replace it
			content := b.codeBuffer.String()
			if lastNewline := strings.LastIndex(strings.TrimSuffix(content, "\n"), "\n"); lastNewline != -1 {
				b.codeBuffer.Reset()
				b.codeBuffer.WriteString(content[:lastNewline+1])
			} else {
				b.codeBuffer.Reset()
			}
		}

		b.codeBuffer.WriteString(line)
		b.codeBuffer.WriteString("\n")

		if isCodeFence {
			// Closing fence - output the entire code block
			content := b.codeBuffer.String()
			// Remove trailing newline for output
			content = strings.TrimSuffix(content, "\n")
			err := b.output(content, b.codeBlockSent)
			b.codeBuffer.Reset()
			b.inCodeBlock = false
			b.codeBlockSent = false
			return err
		}
		// Still in code block, don't output yet
		return nil
	}

	if b.inTable {
		// We're inside a table
		if isTableRow {
			// Continue buffering table rows
			if update && len(b.tableLines) > 0 {
				// This is an update to the previous partial line - replace last line
				b.tableLines[len(b.tableLines)-1] = line
			} else {
				b.tableLines = append(b.tableLines, line)
			}
			return nil
		}
		// Line doesn't start with | - table ended
		// Output the buffered table
		err := b.outputTable()
		if err != nil {
			return err
		}
		// Now process the current line normally (fall through)
	}

	// Not in a code block or table
	if isCodeFence {
		// Opening fence - start buffering code block
		b.inCodeBlock = true
		b.codeBuffer.Reset()
		b.codeBuffer.WriteString(line)
		b.codeBuffer.WriteString("\n")
		b.codeBlockSent = false
		return nil
	}

	if isTableRow {
		// Start of a table - begin buffering
		b.inTable = true
		b.tableLines = []string{line}
		b.codeBlockSent = false
		return nil
	}

	// Regular line - pass through
	return b.output(line, update)
}

// outputTable outputs the buffered table content.
// If tableOutput callback is set, it's called with the table lines.
// Otherwise, falls back to wrapping in a code block.
// Must be called with mutex held.
func (b *BlockBuffer) outputTable() error {
	defer func() {
		b.tableLines = nil
		b.inTable = false
		b.codeBlockSent = false
	}()

	if len(b.tableLines) == 0 {
		return nil
	}

	// If tableOutput callback is set, use it
	if b.tableOutput != nil {
		return b.tableOutput(b.tableLines)
	}

	// Fallback: wrap table in code block for monospace rendering
	content := strings.Join(b.tableLines, "\n")
	wrapped := "```\n" + content + "\n```"
	return b.output(wrapped, b.codeBlockSent)
}

// Write adds a fragment to the buffer.
func (b *BlockBuffer) Write(fragment string) error {
	return b.lineBuffer.Write(fragment)
}

// Flush outputs any remaining buffered content immediately.
func (b *BlockBuffer) Flush() error {
	// First flush the line buffer to get any partial lines
	if err := b.lineBuffer.Flush(); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// If we're still in a code block, output what we have
	if b.inCodeBlock && b.codeBuffer.Len() > 0 {
		content := b.codeBuffer.String()
		content = strings.TrimSuffix(content, "\n")
		err := b.output(content, b.codeBlockSent)
		b.codeBuffer.Reset()
		b.inCodeBlock = false
		b.codeBlockSent = false
		return err
	}

	// If we're still in a table, output what we have
	if b.inTable && len(b.tableLines) > 0 {
		return b.outputTable()
	}

	return nil
}
