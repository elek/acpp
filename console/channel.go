package console

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/coder/acp-go-sdk"
	"github.com/elek/acpp/channel"
)

type Console struct {
	buffers  map[channel.SourceID]*channel.BlockBuffer
	bufferMu sync.Mutex
	inputCh  chan channel.Input
}

var _ channel.Channel = (*Console)(nil)

func CreateChannel(ctx context.Context) (channel.Channel, error) {
	c := &Console{
		buffers: make(map[channel.SourceID]*channel.BlockBuffer),
		inputCh: make(chan channel.Input, 16),
	}
	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Println(">")
			line, err := reader.ReadString('\n')
			if err != nil {
				close(c.inputCh)
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			input := channel.Input{Source: channel.SourceID("console")}
			if strings.HasPrefix(line, "/") {
				input.Command = line
			} else {
				input.Message = line
			}
			c.inputCh <- input
		}
	}()
	return c, nil
}

func (c *Console) Input() <-chan channel.Input {
	return c.inputCh
}

func (c *Console) Close() error {
	return nil
}

func (c *Console) GetBufferedWriter(source channel.SourceID) *channel.BlockBuffer {
	c.bufferMu.Lock()
	defer c.bufferMu.Unlock()

	buffer, exists := c.buffers[source]
	if !exists {
		buffer = channel.NewBlockBuffer(func(line string, update bool) error {
			if update {
				fmt.Print("\033[2K\r")
			} else {
				fmt.Println()
			}
			fmt.Print(line)
			return nil
		})
		c.buffers[source] = buffer
	}
	return buffer
}

func (c *Console) SendTextFragment(source channel.SourceID, s string) error {
	return c.GetBufferedWriter(source).Write(s)
}

func (c *Console) SendThoughtFragment(source channel.SourceID, s string) error {
	return c.SendTextFragment(source, s)
}

func (c *Console) SendTextMessage(source channel.SourceID, s string) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return err
	}
	fmt.Println(s)
	fmt.Print(s)
	return err

}

func (c *Console) StartConversation(source channel.SourceID) error {
	return nil
}

func (c *Console) FinishConversation(source channel.SourceID, response acp.PromptResponse) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return err
	}
	fmt.Println()
	return nil
}

func (c *Console) SendImage(source channel.SourceID, mimeType string, data []byte) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return err
	}
	fmt.Printf("[image: %s, %d bytes]\n", mimeType, len(data))
	return nil
}

func (c *Console) SendFile(source channel.SourceID, filename string, content []byte) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return err
	}
	fmt.Println(string(content))
	return nil
}

func (c *Console) SendToolUsage(source channel.SourceID, toolCallID string) channel.ToolUsageUpdater {
	return func(usage channel.ToolUsage) error {
		fmt.Println(source, toolCallID, usage)
		return nil
	}
}

func (c *Console) GetTopic(source channel.SourceID) (string, error) {
	return "", nil
}

func (c *Console) GetChannelName(source channel.SourceID) (string, error) {
	return string(source), nil
}

func (c *Console) AskYesNo(source channel.SourceID, question string) (bool, error) {
	fmt.Printf("%s [y/N]: ", question)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes", nil
}

func (c *Console) SendPlanUpdate(source channel.SourceID, plan channel.PlanUpdate) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return err
	}

	// Count completed and total
	completed := 0
	total := len(plan.Entries)
	for _, entry := range plan.Entries {
		if entry.Status == "completed" {
			completed++
		}
	}

	fmt.Printf("\n📋 Plan (%d/%d completed)\n", completed, total)
	for _, entry := range plan.Entries {
		var emoji string
		switch entry.Status {
		case "completed":
			emoji = "✅"
		case "in_progress":
			emoji = "🔄"
		default:
			emoji = "⏳"
		}
		fmt.Printf("%s %s\n", emoji, entry.Content)
	}
	fmt.Println()
	return nil
}
