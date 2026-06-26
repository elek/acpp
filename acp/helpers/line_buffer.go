package helpers

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
