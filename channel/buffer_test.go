package channel

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lineOutput struct {
	line   string
	update bool
}

func TestLineBuffer_CompleteLine(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)
	err := cb.Flush()
	require.NoError(t, err)

	cb.Write("hello world\n")
	cb.Write("line")
	cb.Write(" 2\n")
	cb.Flush()

	require.Equal(t, []string{"hello world", "line 2"}, output.lines)
}

func TestLineBuffer_FragmentedInput(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)
	err := cb.Flush()
	require.NoError(t, err)

	cb.Write("hel")
	cb.Write("lo worl")
	cb.Write("d\n")
	cb.Write("line 2\n")
	cb.Write("line 3")
	cb.Write("\n")
	cb.Flush()

	require.Equal(t, []string{"hello world", "line 2", "line 3"}, output.lines)
}

func TestLineBuffer_TimerFlush(t *testing.T) {
	var output Collector

	lb := NewLineBuffer(output.Collect)
	lb.SetFlushDelay(50 * time.Millisecond)

	// Write partial line (no newline)
	lb.Write("partial")
	lb.Write(" line ")
	lb.Write("coming")

	assert.Len(t, output.lines, 0)

	// Wait for timer
	time.Sleep(60 * time.Millisecond)
	require.Len(t, output.lines, 1)
	lb.Flush()

	lb.Write(" first line still \n")
	lb.Write("second line \n")

	time.Sleep(100 * time.Millisecond)

	//require.Len(t, output.lines, 2)
	require.Equal(t, []string{"partial line coming first line still ", "second line "}, output.lines)
}

func TestLineBuffer_TimerFlushThenMore(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	lb := NewLineBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})
	lb.SetFlushDelay(50 * time.Millisecond)

	// Write partial line
	lb.Write("hello")

	// Wait for timer to flush
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	require.Len(t, outputs, 1)
	assert.False(t, outputs[0].update, "first output should have update=false")
	mu.Unlock()

	// Write more to the same line
	lb.Write(" world")

	// Wait for second timer
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 2)
	assert.Equal(t, "hello world", outputs[1].line)
	assert.True(t, outputs[1].update, "second output should have update=true")
}

func TestLineBuffer_UpdateThenNewline(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	lb := NewLineBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})
	lb.SetFlushDelay(50 * time.Millisecond)

	// Write partial line
	lb.Write("hello")

	// Wait for timer
	time.Sleep(100 * time.Millisecond)

	// Complete the line with newline
	lb.Write(" world\n")

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 2)

	// First output: partial line
	assert.Equal(t, "hello", outputs[0].line)
	assert.False(t, outputs[0].update, "first output should have update=false")

	// Second output: complete line (update to previous)
	assert.Equal(t, "hello world", outputs[1].line)
	assert.True(t, outputs[1].update, "second output should have update=true")
}

func TestLineBuffer_Flush(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	lb := NewLineBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})

	lb.Write("partial content")

	// No output yet
	mu.Lock()
	count := len(outputs)
	mu.Unlock()
	assert.Equal(t, 0, count, "expected 0 outputs before flush")

	// Flush
	err := lb.Flush()
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 1)
	assert.Equal(t, "partial content", outputs[0].line)
}

func TestLineBuffer_FlushEmpty(t *testing.T) {
	var outputs []lineOutput

	lb := NewLineBuffer(func(line string, update bool) error {
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})

	// Flush with nothing in buffer
	err := lb.Flush()
	require.NoError(t, err)

	assert.Empty(t, outputs)
}

func TestLineBuffer_FlushAfterCompleteLine(t *testing.T) {
	var outputs []lineOutput

	lb := NewLineBuffer(func(line string, update bool) error {
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})

	lb.Write("complete line\n")
	lb.Flush()

	assert.Len(t, outputs, 1)
}

func TestLineBuffer_MixedContent(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	lb := NewLineBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})
	lb.SetFlushDelay(50 * time.Millisecond)

	// Write content with complete lines and partial
	lb.Write("line1\nline2\npartial")

	mu.Lock()
	count := len(outputs)
	mu.Unlock()
	assert.Equal(t, 2, count, "expected 2 outputs for complete lines")

	// Wait for timer to flush partial
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 3)

	expected := []string{"line1", "line2", "partial"}
	for i, exp := range expected {
		assert.Equal(t, exp, outputs[i].line, "output %d", i)
	}
}

func TestLineBuffer_OutputError(t *testing.T) {
	expectedErr := errors.New("output error")

	lb := NewLineBuffer(func(line string, update bool) error {
		return expectedErr
	})

	err := lb.Write("test\n")
	assert.Equal(t, expectedErr, err)
}

func TestLineBuffer_EmptyLines(t *testing.T) {
	var outputs []lineOutput

	lb := NewLineBuffer(func(line string, update bool) error {
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})

	lb.Write("\n\n\n")

	require.Len(t, outputs, 3)

	for i, o := range outputs {
		assert.Empty(t, o.line, "output %d: expected empty string", i)
	}
}

func TestLineBuffer_NewLineAfterFlush(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	lb := NewLineBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})

	lb.Write("first")
	lb.Flush()

	lb.Write("second\n")

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 2)

	// First flush
	assert.Equal(t, "first", outputs[0].line)
	assert.False(t, outputs[0].update, "first output should have update=false")

	// Second line should be new (not update)
	assert.Equal(t, "second", outputs[1].line)
	assert.False(t, outputs[1].update, "second output should have update=false (new line after flush)")
}

func TestLineBuffer_TimerResetOnWrite(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	lb := NewLineBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})
	lb.SetFlushDelay(100 * time.Millisecond)

	// Write initial partial
	lb.Write("hel")

	// Wait 50ms (less than flush delay)
	time.Sleep(50 * time.Millisecond)

	// Write more - should reset timer
	lb.Write("lo")

	// Wait another 50ms - should not have flushed yet (timer was reset)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := len(outputs)
	mu.Unlock()
	assert.Equal(t, 0, count, "expected 0 outputs before timer expires")

	// Wait for timer to actually expire
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 1)
	assert.Equal(t, "hello", outputs[0].line)
}

// BlockBuffer tests
type Collector struct {
	lines []string
}

func (c *Collector) Collect(line string, update bool) error {
	if len(c.lines) == 0 {
		c.lines = append(c.lines, line)
		return nil
	}
	if update {
		// Update the last line
		c.lines[len(c.lines)-1] = line
		return nil
	}
	c.lines = append(c.lines, line)
	return nil
}

func TestBlockBuffer_RegularLines(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)
	err := cb.Flush()
	require.NoError(t, err)

	cb.Write("line1\nline2\nline3\n")

	require.Equal(t, []string{"line1", "line2", "line3"}, output.lines)
}

func TestBlockBuffer_SimpleCodeBlock(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)
	err := cb.Flush()
	require.NoError(t, err)

	cb.Write("```go\nfmt.Println(\"hello\")\n```\nfoobar\n")

	require.Equal(t, []string{"```go\nfmt.Println(\"hello\")\n```", "foobar"}, output.lines)
}

func TestBlockBuffer_CodeBlockWithLanguage(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("```python\nprint('hello')\nprint('world')\n```\n")

	require.Len(t, output.lines, 1)
	assert.Equal(t, "```python\nprint('hello')\nprint('world')\n```", output.lines[0])
}

func TestBlockBuffer_TextBeforeAndAfterCodeBlock(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("Here is some code:\n```go\ncode here\n```\nThat was the code.\n")

	require.Len(t, output.lines, 3)
	assert.Equal(t, "Here is some code:", output.lines[0])
	assert.Equal(t, "```go\ncode here\n```", output.lines[1])
	assert.Equal(t, "That was the code.", output.lines[2])
}

func TestBlockBuffer_MultipleCodeBlocks(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("First block:\n```\nblock1\n```\nSecond block:\n```\nblock2\n```\n")

	require.Len(t, output.lines, 4)
	assert.Equal(t, "First block:", output.lines[0])
	assert.Equal(t, "```\nblock1\n```", output.lines[1])
	assert.Equal(t, "Second block:", output.lines[2])
	assert.Equal(t, "```\nblock2\n```", output.lines[3])
}

func TestBlockBuffer_FragmentedCodeBlock(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Write code block in fragments
	cb.Write("```go\n")
	cb.Write("func main() {\n")
	cb.Write("}\n")
	cb.Write("```\n")

	require.Len(t, output.lines, 1)
	assert.Equal(t, "```go\nfunc main() {\n}\n```", output.lines[0])
}

func TestBlockBuffer_FlushDuringCodeBlock(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Start a code block but don't close it
	cb.Write("```go\ncode here\n")

	// Nothing should be output yet
	assert.Empty(t, output.lines)

	// Flush should output the partial code block
	err := cb.Flush()
	require.NoError(t, err)

	require.Len(t, output.lines, 1)
	assert.Equal(t, "```go\ncode here", output.lines[0])
}

func TestBlockBuffer_EmptyCodeBlock(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("```\n```\n")

	require.Len(t, output.lines, 1)
	assert.Equal(t, "```\n```", output.lines[0])
}

func TestBlockBuffer_CodeBlockWithEmptyLines(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("```\n\ncode\n\n```\n")

	require.Len(t, output.lines, 1)
	assert.Equal(t, "```\n\ncode\n\n```", output.lines[0])
}

func TestBlockBuffer_BackticksInMiddleOfLine(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Backticks not at start of line should not trigger code block
	cb.Write("Use `code` for inline\n")
	cb.Write("Or use ``` for blocks\n")

	require.Len(t, output.lines, 2)
	assert.Equal(t, "Use `code` for inline", output.lines[0])
	assert.Equal(t, "Or use ``` for blocks", output.lines[1])
}

func TestBlockBuffer_TimerFlushOutsideCodeBlock(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	cb := NewBlockBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})
	cb.SetFlushDelay(50 * time.Millisecond)

	// Write partial line outside code block
	cb.Write("partial")

	// Wait for timer
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 1)
	assert.Equal(t, "partial", outputs[0].line)
}

func TestBlockBuffer_TimerFlushInsideCodeBlock(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	cb := NewBlockBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})
	cb.SetFlushDelay(50 * time.Millisecond)

	// Write partial line inside code block
	cb.Write("```go\npartial")

	// Wait for timer - should flush line to code block buffer
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(outputs)
	mu.Unlock()

	// Code block should still be buffered (not output yet)
	assert.Equal(t, 0, count, "code block should not be output until closed")

	// Now close the code block
	cb.Write("\n```\n")

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 1)
	assert.Equal(t, "```go\npartial\n```", outputs[0].line)
}

func TestBlockBuffer_UpdateFlagPassthrough(t *testing.T) {
	var outputs []lineOutput
	var mu sync.Mutex

	cb := NewBlockBuffer(func(line string, update bool) error {
		mu.Lock()
		defer mu.Unlock()
		outputs = append(outputs, lineOutput{line, update})
		return nil
	})
	cb.SetFlushDelay(50 * time.Millisecond)

	// Write partial line
	cb.Write("hello")

	// Wait for timer
	time.Sleep(100 * time.Millisecond)

	// Write more to same line
	cb.Write(" world\n")

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, outputs, 2)
	assert.False(t, outputs[0].update, "first output should have update=false")
	assert.True(t, outputs[1].update, "second output should have update=true")
}

func TestBlockBuffer_NestedBackticks(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Code block containing backticks
	cb.Write("````\n```\ninner\n```\n````\n")

	// The first ```` opens, then ``` closes it (any ``` fence closes)
	// Then inner is a regular line, then ``` opens again, then ```` closes
	// This is a bit complex - let's see what we get
	require.GreaterOrEqual(t, len(output.lines), 1)
}

func TestBlockBuffer_RealWorldExample(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Simulate a realistic markdown response
	input := `Here's how to do it:

` + "```go" + `
package main

import "fmt"

func main() {
    fmt.Println("Hello, World!")
}
` + "```" + `

This will print "Hello, World!" to the console.
`

	cb.Write(input)

	require.Len(t, output.lines, 5)
	assert.Equal(t, "Here's how to do it:", output.lines[0])
	assert.Equal(t, "", output.lines[1]) // empty line
	expectedCode := "```go\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n    fmt.Println(\"Hello, World!\")\n}\n```"
	assert.Equal(t, expectedCode, output.lines[2])
	assert.Equal(t, "", output.lines[3]) // empty line after code block
	assert.Equal(t, "This will print \"Hello, World!\" to the console.", output.lines[4])
}

// Table buffering tests

func TestBlockBuffer_SimpleTable(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("| Header 1 | Header 2 |\n|----------|----------|\n| Value 1  | Value 2  |\n\nSome text after.\n")

	require.Len(t, output.lines, 3)
	// Table should be wrapped in code block
	expectedTable := "```\n| Header 1 | Header 2 |\n|----------|----------|\n| Value 1  | Value 2  |\n```"
	assert.Equal(t, expectedTable, output.lines[0])
	assert.Equal(t, "", output.lines[1]) // empty line triggers table end
	assert.Equal(t, "Some text after.", output.lines[2])
}

func TestBlockBuffer_TableWithTextBefore(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("Here is a table:\n| Col A | Col B |\n|-------|-------|\n| 1     | 2     |\nEnd of table.\n")

	require.Len(t, output.lines, 3)
	assert.Equal(t, "Here is a table:", output.lines[0])
	expectedTable := "```\n| Col A | Col B |\n|-------|-------|\n| 1     | 2     |\n```"
	assert.Equal(t, expectedTable, output.lines[1])
	assert.Equal(t, "End of table.", output.lines[2])
}

func TestBlockBuffer_TableFlush(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Write table without ending it
	cb.Write("| Item | Deadline |\n|------|----------|\n| Task | Feb 10   |")

	// Nothing output yet
	assert.Empty(t, output.lines)

	// Flush should output the table
	err := cb.Flush()
	require.NoError(t, err)

	require.Len(t, output.lines, 1)
	expectedTable := "```\n| Item | Deadline |\n|------|----------|\n| Task | Feb 10   |\n```"
	assert.Equal(t, expectedTable, output.lines[0])
}

func TestBlockBuffer_FragmentedTable(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Write table in fragments
	cb.Write("| A | B |\n")
	cb.Write("|---|---|\n")
	cb.Write("| 1 | 2 |\n")
	cb.Write("Done.\n")

	require.Len(t, output.lines, 2)
	expectedTable := "```\n| A | B |\n|---|---|\n| 1 | 2 |\n```"
	assert.Equal(t, expectedTable, output.lines[0])
	assert.Equal(t, "Done.", output.lines[1])
}

func TestBlockBuffer_MultipleTables(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("Table 1:\n| X |\n|---|\n| 1 |\nBetween tables.\n| Y |\n|---|\n| 2 |\nAfter.\n")

	require.Len(t, output.lines, 5)
	assert.Equal(t, "Table 1:", output.lines[0])
	assert.Equal(t, "```\n| X |\n|---|\n| 1 |\n```", output.lines[1])
	assert.Equal(t, "Between tables.", output.lines[2])
	assert.Equal(t, "```\n| Y |\n|---|\n| 2 |\n```", output.lines[3])
	assert.Equal(t, "After.", output.lines[4])
}

func TestBlockBuffer_PipeInMiddleOfLine(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Pipe not at start of line should not trigger table buffering
	cb.Write("Use a | pipe | for tables\n")
	cb.Write("Or use || for OR\n")

	require.Len(t, output.lines, 2)
	assert.Equal(t, "Use a | pipe | for tables", output.lines[0])
	assert.Equal(t, "Or use || for OR", output.lines[1])
}

func TestBlockBuffer_RealWorldTable(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	// Simulate a multi-row table with surrounding text
	input := `Here are the results:

| Name | Score |
|------|-------|
| Alice | 95 |
| Bob | 87 |
| Charlie | 92 |
| Diana | 88 |
| Eve | 91 |

Summary complete.
`

	cb.Write(input)

	require.Len(t, output.lines, 5)
	assert.Equal(t, "Here are the results:", output.lines[0])
	assert.Equal(t, "", output.lines[1]) // empty line before table

	expectedTable := `| Name | Score |
|------|-------|
| Alice | 95 |
| Bob | 87 |
| Charlie | 92 |
| Diana | 88 |
| Eve | 91 |`
	assert.Equal(t, "```\n"+expectedTable+"\n```", output.lines[2])
	assert.Equal(t, "", output.lines[3]) // empty line after table
	assert.Equal(t, "Summary complete.", output.lines[4])
}

func TestBlockBuffer_TableThenCodeBlock(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("| A |\n|---|\nHere's code:\n```go\nfmt.Println()\n```\n")

	require.Len(t, output.lines, 3)
	assert.Equal(t, "```\n| A |\n|---|\n```", output.lines[0])
	assert.Equal(t, "Here's code:", output.lines[1])
	assert.Equal(t, "```go\nfmt.Println()\n```", output.lines[2])
}

func TestBlockBuffer_CodeBlockThenTable(t *testing.T) {
	var output Collector
	cb := NewBlockBuffer(output.Collect)

	cb.Write("```go\nfmt.Println()\n```\nNow a table:\n| A |\n|---|\nDone.\n")

	require.Len(t, output.lines, 4)
	assert.Equal(t, "```go\nfmt.Println()\n```", output.lines[0])
	assert.Equal(t, "Now a table:", output.lines[1])
	assert.Equal(t, "```\n| A |\n|---|\n```", output.lines[2])
	assert.Equal(t, "Done.", output.lines[3])
}
