package discord

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/coder/acp-go-sdk"
	"github.com/elek/acpp/channel"
	"github.com/pkg/errors"
)

// DiscordChannel adapts a Discord channel to the generic channel interface.
type DiscordChannel struct {
	session   *discordgo.Session
	commands  []*discordgo.ApplicationCommand
	channelID string
	inputCh   chan channel.Input

	// Code block buffering per source channel
	buffers  map[channel.SourceID]*channel.BlockBuffer
	bufferMu sync.Mutex

	// Typing indicator management
	typing *TypingIndicator
}

var _ channel.Channel = (*DiscordChannel)(nil)

// NewDiscordChannel creates a DiscordChannel wrapper for a channel ID.
func NewDiscordChannel(token string) channel.CreateChannel {
	return func(ctx context.Context) (channel.Channel, error) {
		dg, err := discordgo.New("Bot " + token)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		b := &DiscordChannel{
			session: dg,
			inputCh: make(chan channel.Input, 16),
			buffers: make(map[channel.SourceID]*channel.BlockBuffer),
			typing: NewTypingIndicator(func(channelID string) error {
				return dg.ChannelTyping(channelID)
			}),
		}
		dg.Identify.Intents = discordgo.IntentsGuilds |
			discordgo.IntentsGuildMessages |
			discordgo.IntentMessageContent |
			discordgo.IntentGuildMessageReactions

		if err := b.session.Open(); err != nil {
			return nil, errors.WithStack(err)

		}
		dg.AddHandler(b.handleReady)
		dg.AddHandler(b.handleInteraction)
		dg.AddHandler(b.handleMessage)
		return b, nil
	}
}

func (c *DiscordChannel) handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot {
		return
	}

	c.channelID = m.ChannelID
	source := channel.SourceID(m.ChannelID)

	// Build message content from text and attachments
	content := m.Content
	var images []channel.ImageData

	for _, att := range m.Attachments {
		if isTextAttachment(att.Filename) {
			text, err := downloadAttachment(att.URL)
			if err != nil {
				log.Printf("Error downloading attachment %s: %v", att.Filename, err)
				continue
			}
			if content != "" {
				content += "\n\n"
			}
			content += text
		} else if isImageAttachment(att.ContentType) {
			imgData, err := downloadImageAttachment(att.URL, att.ContentType)
			if err != nil {
				log.Printf("Error downloading image %s: %v", att.Filename, err)
				continue
			}
			images = append(images, imgData)
		}
	}

	// Ignore empty messages (no text, no valid attachments, no images)
	if content == "" && len(images) == 0 {
		return
	}

	// Check if it's a command (starts with /)
	trimmedContent := strings.TrimSpace(content)
	inlineCommand := strings.Trim(trimmedContent, string([]byte{96}))
	input := channel.Input{Source: source, Images: images}
	if strings.HasPrefix(inlineCommand, "/") {
		input.Command = inlineCommand
	} else {
		input.Message = content
	}
	c.inputCh <- input
}

// isTextAttachment checks if the filename has a supported text extension.
func isTextAttachment(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".txt") || strings.HasSuffix(lower, ".md")
}

// isImageAttachment checks if the content type is a supported image format.
func isImageAttachment(contentType string) bool {
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// downloadAttachment fetches the content of a text file attachment.
func downloadAttachment(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", errors.Wrap(err, "failed to download attachment")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", errors.Errorf("failed to download attachment: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "failed to read attachment body")
	}

	return string(body), nil
}

// downloadImageAttachment downloads an image attachment with a size limit.
func downloadImageAttachment(url string, contentType string) (channel.ImageData, error) {
	resp, err := http.Get(url)
	if err != nil {
		return channel.ImageData{}, errors.Wrap(err, "failed to download image")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return channel.ImageData{}, errors.Errorf("download image: status %d", resp.StatusCode)
	}

	const maxSize = 20 * 1024 * 1024 // 20MB limit
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
	if err != nil {
		return channel.ImageData{}, errors.Wrap(err, "failed to read image body")
	}

	// Detect actual MIME type from bytes — Discord may report wrong content type
	// (e.g., image/webp for a PNG), which causes API rejections.
	detected := http.DetectContentType(data)
	if isImageAttachment(detected) {
		contentType = detected
	}

	return channel.ImageData{Data: data, MimeType: contentType}, nil
}

func (c *DiscordChannel) Input() <-chan channel.Input {
	return c.inputCh
}

func (c *DiscordChannel) Close() error {
	return c.session.Close()
}

func (c *DiscordChannel) SendTextMessage(source channel.SourceID, content string) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return errors.WithStack(err)
	}
	for _, chunk := range splitMessage(content) {
		_, err = c.session.ChannelMessageSend(string(source), chunk)
		if err != nil {
			return errors.WithStack(err)
		}
	}
	// Re-trigger typing indicator after sending (Discord clears it on message send)
	c.typing.Refresh(source)
	return nil
}

func (c *DiscordChannel) SendTextFragment(source channel.SourceID, s string) error {
	err := c.GetBufferedWriter(source).Write(s)
	return err
}

func (c *DiscordChannel) SendThoughtFragment(source channel.SourceID, s string) error {
	return c.SendTextFragment(source, s)
}

func (c *DiscordChannel) GetBufferedWriter(source channel.SourceID) *channel.BlockBuffer {
	c.bufferMu.Lock()
	defer c.bufferMu.Unlock()

	buffer, exists := c.buffers[source]
	if !exists {
		var lastMsgID string
		buffer = channel.NewBlockBuffer(func(line string, update bool) error {
			line = stripCodeBlockLanguage(line)
			chunks := splitMessage(line)
			if update && lastMsgID != "" && len(chunks) == 1 {
				_, err := c.session.ChannelMessageEdit(string(source), lastMsgID, chunks[0])
				return err
			}
			// Send first chunk as update if applicable, rest as new messages
			for i, chunk := range chunks {
				if i == 0 && update && lastMsgID != "" {
					_, err := c.session.ChannelMessageEdit(string(source), lastMsgID, chunk)
					if err != nil {
						return err
					}
				} else {
					resp, err := c.session.ChannelMessageSend(string(source), chunk)
					if err != nil {
						return errors.WithStack(err)
					}
					lastMsgID = resp.ID
					c.typing.Refresh(source)
				}
			}
			return nil
		})

		// Set up table rendering with embeds
		buffer.SetTableOutput(func(lines []string) error {
			table := channel.ParseMarkdownTable(lines)
			if table == nil {
				// Failed to parse, fall back to code block
				content := "```\n" + strings.Join(lines, "\n") + "\n```"
				_, err := c.session.ChannelMessageSend(string(source), content)
				if err != nil {
					return errors.WithStack(err)
				}
				c.typing.Refresh(source)
				return nil
			}

			if table.CanRenderAsEmbed() {
				// Render as embed
				embed := table.ToEmbed()
				_, err := c.session.ChannelMessageSendEmbed(string(source), embed)
				if err != nil {
					return errors.WithStack(err)
				}
				c.typing.Refresh(source)
				return nil
			}

			// Table too large for embed, use code block
			content := table.ToCodeBlock()
			_, err := c.session.ChannelMessageSend(string(source), content)
			if err != nil {
				return errors.WithStack(err)
			}
			c.typing.Refresh(source)
			return nil
		})

		c.buffers[source] = buffer
	}
	return buffer
}

func (c *DiscordChannel) StartConversation(source channel.SourceID) error {
	_ = c.GetBufferedWriter(source)
	c.typing.Start(source)
	return nil
}

func (c *DiscordChannel) FinishConversation(source channel.SourceID, resp acp.PromptResponse) error {
	c.typing.Stop(source)

	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (c *DiscordChannel) SendToolUsage(s channel.SourceID, toolCallID string) channel.ToolUsageUpdater {
	var t channel.ToolUsage
	var msgId string
	return func(toolUsage channel.ToolUsage) error {
		if toolUsage.Name != "" {
			t.Name = toolUsage.Name
		}
		if toolUsage.Title != "" {
			t.Title = toolUsage.Title
		}
		if len(toolUsage.Input) > 0 {
			if t.Input == nil {
				t.Input = make(map[string]string)
			}
			for k, v := range toolUsage.Input {
				t.Input[k] = v
			}
		}
		if toolUsage.Status != "" {
			t.Status = toolUsage.Status
		}
		if toolUsage.ToolKind != "" {
			t.ToolKind = toolUsage.ToolKind
		}
		content := formatToolUsage(t)
		if content == "" {
			return nil
		}

		if msgId == "" {
			// First call: send a new message
			msg, err := c.session.ChannelMessageSend(string(s), content)
			if err != nil {
				return err
			}
			msgId = msg.ID
			// Re-trigger typing indicator after sending
			c.typing.Refresh(s)
		} else {
			// Subsequent calls: edit the existing message
			_, err := c.session.ChannelMessageEdit(string(s), msgId, content)
			if err != nil {
				return err
			}
		}
		return nil
	}
}

// toolEmojis maps tool kinds to their display emojis
var toolEmojis = map[string]string{
	"read":    "📖",
	"search":  "🔍",
	"think":   "🤖",
	"execute": "⚡",
	"edit":    "✏️",
	"fetch":   "🌐",
}

// statusEmojis maps status to their display emojis
var statusEmojis = map[string]string{
	"pending":     "⏳",
	"in-progress": "🔄",
	"completed":   "✅",
	"failed":      "❌",
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
		// Reserve space for closing fence if we might need it
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
			// Even if not currently in a code block, the chunk we're about
			// to cut may contain an opening fence, so reserve space.
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

		// Determine if this chunk ends inside a code block by counting
		// fences relative to the state we entered with.
		endsInCodeBlock := inCodeBlock != (strings.Count(chunk, "```")%2 != 0)

		if endsInCodeBlock {
			chunk += "\n```"
		}

		chunks = append(chunks, chunk)

		// Advance past the cut point
		s = s[cut:]
		if len(s) > 0 && s[0] == '\n' {
			s = s[1:]
		}

		// Reopen code block in next chunk if needed
		if endsInCodeBlock {
			s = "```\n" + s
		}

		inCodeBlock = false // we always close/reopen, so next iteration starts clean
	}

	return chunks
}

// isInsideCodeBlock returns true if the text ends with an unclosed code block.
func isInsideCodeBlock(s string) bool {
	count := strings.Count(s, "```")
	return count%2 != 0
}

// stripCodeBlockLanguage removes the language identifier from markdown code
// block fences (e.g., "```yaml\n..." becomes "```\n..."). Discord doesn't
// render language-specific syntax highlighting, so the identifier is noise.
func stripCodeBlockLanguage(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Find the end of the first line
	newline := strings.Index(s, "\n")
	if newline == -1 {
		return s
	}
	// If the opening fence is just "```", nothing to strip
	fence := s[:newline]
	if fence == "```" {
		return s
	}
	return "```" + s[newline:]
}

func formatToolUsage(toolUsage channel.ToolUsage) string {
	var lines []string

	// First line: emoji + bold type + non-bold title + status
	var firstLine strings.Builder

	// Special handling for Skill tool: display "🎯 skill: skill_name"
	if skillName, ok := toolUsage.Input["skill"]; ok {
		firstLine.WriteString("🎯 skill: " + skillName)
	} else {
		// Add tool emoji if available
		if emoji, ok := toolEmojis[toolUsage.ToolKind]; ok {
			firstLine.WriteString(emoji + " ")
		} else {
			firstLine.WriteString(toolUsage.ToolKind + " ")
		}

		// Add non-bold title
		if toolUsage.Title != "" {
			firstLine.WriteString(" " + toolUsage.Title)
		}
	}

	// Add status with emoji
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

	// Second line: input parameters (only show if title is NOT set)
	if len(toolUsage.Input) > 0 && toolUsage.Title == "" {
		// Sort keys for consistent output
		keys := make([]string, 0, len(toolUsage.Input))
		for key := range toolUsage.Input {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		var params []string
		for _, key := range keys {
			value := toolUsage.Input[key]
			// Truncate long values
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

func (c *DiscordChannel) SendImage(source channel.SourceID, mimeType string, data []byte) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return errors.WithStack(err)
	}
	ext := ".png"
	switch mimeType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	_, err = c.session.ChannelFileSend(string(source), "image"+ext, bytes.NewReader(data))
	c.typing.Refresh(source)
	return err
}

func (c *DiscordChannel) SendFile(source channel.SourceID, filename string, content []byte) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = c.session.ChannelFileSend(string(source), filename, bytes.NewReader(content))
	c.typing.Refresh(source)
	return err
}

func (c *DiscordChannel) AskYesNo(source channel.SourceID, question string) (bool, error) {
	msg, err := c.session.ChannelMessageSend(string(source), question+" React with \u2705 to approve or \u274c to deny.")
	if err != nil {
		return false, errors.WithStack(err)
	}

	// Add reaction options
	_ = c.session.MessageReactionAdd(string(source), msg.ID, "\u2705")
	_ = c.session.MessageReactionAdd(string(source), msg.ID, "\u274c")

	// Wait for a reaction from a non-bot user
	responseCh := make(chan bool, 1)
	removeHandler := c.session.AddHandler(func(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
		if r.MessageID != msg.ID || r.UserID == s.State.User.ID {
			return
		}
		switch r.Emoji.Name {
		case "\u2705":
			responseCh <- true
		case "\u274c":
			responseCh <- false
		}
	})
	defer removeHandler()

	select {
	case result := <-responseCh:
		return result, nil
	case <-time.After(2 * time.Minute):
		return false, nil
	}
}

func (c *DiscordChannel) StartTyping() error {
	return c.session.ChannelTyping(c.channelID)
}

// registerCommands registers all slash commands globally.
// Global commands may take up to 1 hour to propagate to all guilds.
func (c *DiscordChannel) registerCommands() error {
	appID := c.session.State.User.ID
	for _, cmd := range commandDefinitions {
		_, err := c.session.ApplicationCommandCreate(appID, "", cmd) // "" = global
		if err != nil {
			return fmt.Errorf("failed to register command %s: %w", cmd.Name, err)
		}
		log.Printf("Registered command: %s", cmd.Name)
	}
	return nil
}

// handleInteraction routes incoming interactions to the appropriate handler
func (c *DiscordChannel) handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}

	c.channelID = i.ChannelID
	source := channel.SourceID(i.ChannelID)
	command := "/" + i.ApplicationCommandData().Name

	// Append slash command options as arguments
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.StringValue() != "" {
			command += " " + opt.StringValue()
		}
	}

	// Acknowledge the interaction
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	c.inputCh <- channel.Input{Source: source, Command: command}
}

// handleReady logs when the bot successfully connects
func (c *DiscordChannel) handleReady(s *discordgo.Session, r *discordgo.Ready) {
	log.Printf("Discord bot connected as %s#%s", r.User.Username, r.User.Discriminator)
}

// commandDefinitions defines the slash commands for the bot.
// Global commands may take up to 1 hour to propagate after registration.
var commandDefinitions = []*discordgo.ApplicationCommand{
	{
		Name:        "start",
		Description: "Start an ACP agent session in this channel",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "agent",
				Description: "Agent command to run (default: from channel topic or claude-code-acp)",
				Required:    false,
			},
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "dir",
				Description: "Working directory (default: from channel topic or current directory)",
				Required:    false,
			},
		},
	},
	{
		Name:        "stop",
		Description: "Stop the active agent session in this channel",
	},
	{
		Name:        "status",
		Description: "Show the status of the agent session in this channel",
	},
	{
		Name:        "clear",
		Description: "Clear the agent's conversation history",
	},
	{
		Name:        "exit",
		Description: "Gracefully shutdown the bot application",
	},
	{
		Name:        "get",
		Description: "Show project settings for this channel",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "field",
				Description: "Setting to show (agent, dir, sandbox, permission, repo, env). Omit for all.",
				Required:    false,
			},
		},
	},
	{
		Name:        "set",
		Description: "Update a project setting for this channel",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "field",
				Description: "Setting to change (agent, dir, sandbox, permission, repo, env)",
				Required:    true,
			},
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "value",
				Description: "New value (for env: KEY=VAL to add, omit to clear all)",
				Required:    false,
			},
		},
	},
}

func (c *DiscordChannel) GetTopic(source channel.SourceID) (string, error) {
	ch, err := c.session.Channel(string(source))
	if err != nil {
		return "", errors.WithStack(err)
	}
	return ch.Topic, nil
}

func (c *DiscordChannel) GetChannelName(source channel.SourceID) (string, error) {
	ch, err := c.session.Channel(string(source))
	if err != nil {
		return "", errors.WithStack(err)
	}
	return ch.Name, nil
}

func (c *DiscordChannel) SendPlanUpdate(source channel.SourceID, plan channel.PlanUpdate) error {
	err := c.GetBufferedWriter(source).Flush()
	if err != nil {
		return errors.WithStack(err)
	}

	content := formatPlanUpdate(plan)
	_, err = c.session.ChannelMessageSend(string(source), content)
	if err != nil {
		return errors.WithStack(err)
	}
	c.typing.Refresh(source)
	return nil
}

// planStatusEmojis maps plan entry status to their display emojis
var planStatusEmojis = map[string]string{
	"pending":     "⏳",
	"in_progress": "🔄",
	"completed":   "✅",
}

func formatPlanUpdate(plan channel.PlanUpdate) string {
	var lines []string

	// Count completed and total
	completed := 0
	total := len(plan.Entries)
	for _, entry := range plan.Entries {
		if entry.Status == "completed" {
			completed++
		}
	}

	// Build progress bar
	progressBar := buildProgressBar(completed, total, 10)

	// Header with progress
	header := fmt.Sprintf("📋 Plan (%d/%d completed) %s", completed, total, progressBar)
	lines = append(lines, header)
	lines = append(lines, "")

	// List entries
	for _, entry := range plan.Entries {
		emoji := planStatusEmojis[entry.Status]
		if emoji == "" {
			emoji = "⏳"
		}
		lines = append(lines, fmt.Sprintf("%s %s", emoji, entry.Content))
	}

	// Wrap in code block
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
