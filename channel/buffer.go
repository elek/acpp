package channel

import (
	"strings"
	"sync"
	"time"
)

// LineBuffer accumulates text fragments and outputs complete lines.
// Output is triggered when:
// 1. A line ends with \n
// 2. 5 seconds elapsed since last Write
// 3. Flush is called explicitly
type LineBuffer struct {
	buffer strings.Builder

	output func(line string, update bool) error

	mu           sync.Mutex
	timer        *time.Timer
	currentLine  string // The current line being built (already output once)
	hasOutput    bool   // Whether we've output the current line before
	timerRunning bool
	flushDelay   time.Duration
}

// NewLineBuffer creates a new LineBuffer with the given output function.
// The output function receives line content (without trailing newline) and
// an update flag. When update is false, a new line should be started.
// When update is true, the previous line should be updated with the new content.
func NewLineBuffer(output func(line string, update bool) error) *LineBuffer {
	return &LineBuffer{
		output:     output,
		flushDelay: 5 * time.Second,
	}
}

// SetFlushDelay sets the delay before partial lines are flushed.
// This is primarily useful for testing.
func (b *LineBuffer) SetFlushDelay(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushDelay = d
}

// Write adds a fragment to the buffer. The fragment may contain zero or more newlines.
// Complete lines (ending with \n) are output immediately.
// Partial lines start a timer that will flush after 5 seconds.
func (b *LineBuffer) Write(fragment string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buffer.WriteString(fragment)
	return b.processBuffer()
}

// processBuffer processes the buffer content and outputs complete lines.
// Must be called with mutex held.
func (b *LineBuffer) processBuffer() error {
	content := b.buffer.String()

	for {
		newlineIdx := strings.Index(content, "\n")
		if newlineIdx == -1 {
			break
		}

		// Extract the complete line (without the newline)
		line := content[:newlineIdx]
		content = content[newlineIdx+1:]

		// Combine with any current partial line
		fullLine := b.currentLine + line

		// Output the complete line
		if err := b.output(fullLine, b.hasOutput); err != nil {
			// Restore buffer state on error
			b.buffer.Reset()
			b.buffer.WriteString(b.currentLine)
			b.buffer.WriteString(content)
			return err
		}

		// Reset for next line
		b.currentLine = ""
		b.hasOutput = false
	}

	// Update buffer with remaining content
	b.buffer.Reset()
	b.buffer.WriteString(content)

	// Handle partial line
	if content != "" {
		b.startTimer()
	} else {
		b.stopTimer()
	}

	return nil
}

// timerFlush is called when the timer expires.
func (b *LineBuffer) timerFlush() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.timerRunning = false

	content := b.buffer.String()
	if content == "" {
		return
	}

	// Output the partial line as an update to current line
	fullLine := b.currentLine + content

	if err := b.output(fullLine, b.hasOutput); err != nil {
		// On error, keep the content in the buffer for retry
		return
	}

	// Mark that we've output this line (future writes will be updates)
	b.currentLine = fullLine
	b.hasOutput = true
	b.buffer.Reset()
}

// startTimer starts or restarts the flush timer.
// Must be called with mutex held.
func (b *LineBuffer) startTimer() {
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(b.flushDelay, b.timerFlush)
	b.timerRunning = true
}

// stopTimer stops the flush timer.
// Must be called with mutex held.
func (b *LineBuffer) stopTimer() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
		b.timerRunning = false
	}
}

// Flush outputs any remaining buffered content immediately.
func (b *LineBuffer) Flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.stopTimer()

	content := b.buffer.String()
	if content == "" {
		// Nothing new in the buffer. If currentLine is set, it was already
		// output by a timer flush — don't re-output or reset it.
		return nil
	}

	fullLine := b.currentLine + content
	if err := b.output(fullLine, b.hasOutput); err != nil {
		return err
	}

	b.currentLine = ""
	b.hasOutput = false
	b.buffer.Reset()

	return nil
}

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
