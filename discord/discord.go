package discord

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/acp/helpers"
	"github.com/elek/acpp/router"
	"github.com/elek/acpp/types"
	"github.com/pkg/errors"
)

// DiscordChannel bridges Discord to the router. It is a router.Subscriber: each
// Discord text channel maps 1:1 to a router conversation. Incoming Discord
// messages are submitted to the conversation as prompts; raw ACP updates flowing
// back through Receive are rendered into the originating Discord channel.
//
// All per-target state (renderers, buffers, typing) is keyed by the Discord
// channel ID string — the conversation's rendering target — rather than by a
// channel.SourceID.
type DiscordChannel struct {
	session *discordgo.Session
	router  *router.Router
	ctx     context.Context

	// agent is the default agent command for conversations this bot starts,
	// used unless a project's .acpp.yaml overrides it (resolved by Router.Create).
	agent string
	// searchPaths are the base directories searched for a project directory
	// matching a Discord channel's name.
	searchPaths []string

	// mu guards the conversation<->channel mapping. Correlation is by ProjectID
	// (== the Discord channel name), which is stable and known from creation —
	// unlike the full ConversationMeta, whose SessionID is filled in only after
	// the session initializes. convByChannel holds the meta needed to submit
	// prompts; channelByProject maps an incoming update's ProjectID back to its
	// Discord channel for rendering.
	mu               sync.Mutex
	convByChannel    map[string]types.ConversationMeta
	channelByProject map[string]string

	// Code-block buffering per Discord channel.
	buffers  map[string]*helpers.BlockBuffer
	bufferMu sync.Mutex

	// Per-target renderers translate raw ACP updates into Discord output.
	renderers  map[string]*Renderer
	rendererMu sync.Mutex

	// Typing indicator management (keyed by Discord channel ID).
	typing *TypingIndicator

	// commandsOnce guards the one-time clearing of stale global slash commands
	// so it runs once even though Ready may fire again on reconnects.
	commandsOnce sync.Once
}

var _ RenderSink = (*DiscordChannel)(nil)

// NewDiscordChannel creates a DiscordChannel, opens the Discord session, and
// subscribes it to the router so it renders every conversation's updates. agent
// is the default agent command for conversations this bot starts; searchPaths
// are the base directories searched for a project directory matching a channel's
// name. Per-project defaults (.acpp.yaml) are resolved centrally by Router.Create.
func NewDiscordChannel(token string, agent string, searchPaths []string, r *router.Router) (*DiscordChannel, error) {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	b := &DiscordChannel{
		session:          dg,
		router:           r,
		ctx:              context.Background(),
		agent:            agent,
		searchPaths:      searchPaths,
		convByChannel:    make(map[string]types.ConversationMeta),
		channelByProject: make(map[string]string),
		buffers:          make(map[string]*helpers.BlockBuffer),
		renderers:        make(map[string]*Renderer),
		typing: NewTypingIndicator(func(channelID string) error {
			return dg.ChannelTyping(channelID)
		}),
	}
	dg.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentMessageContent |
		discordgo.IntentGuildMessageReactions

	r.Subscribe(b.Receive)

	if err := b.session.Open(); err != nil {
		return nil, errors.WithStack(err)
	}
	dg.AddHandler(b.handleReady)
	dg.AddHandler(b.handleMessage)
	return b, nil
}

// Receive renders one raw ACP update into the Discord channel backing the
// conversation. Updates for conversations this bot does not own are ignored.
func (c *DiscordChannel) Receive(ctx context.Context, rid *json.RawMessage, id types.ConversationMeta, msg any) {
	// A session swap (e.g. /clear) keeps the same ProjectID but carries a fresh
	// SessionID: refresh the stored meta and start the next turn in a new message.
	if rep, ok := msg.(types.ConversationReplaced); ok {
		c.mu.Lock()
		channelID, mapped := c.channelByProject[rep.New.ProjectID]
		if mapped {
			c.convByChannel[channelID] = rep.New
		}
		c.mu.Unlock()
		if mapped {
			c.resetBuffer(channelID)
			c.sendSystem(channelID, "🔄 session restarted")
		}
		return
	}

	c.mu.Lock()
	channelID, ok := c.channelByProject[id.ProjectID]
	c.mu.Unlock()
	if !ok {
		return
	}

	switch m := msg.(type) {
	case acp.SessionNotification:
		c.rendererFor(channelID).Handle(m.Update)
	case acp.PromptResponse:
		// The turn has completed: stop typing, flush any buffered agent text, then
		// post a summary line with the stop reason and token usage.
		c.typing.Stop(channelID)
		if err := c.GetBufferedWriter(channelID).Flush(); err != nil {
			log.Printf("discord: flush buffer for channel %s: %v", channelID, err)
		}
		if summary := formatPromptResponse(m); summary != "" {
			c.sendSystem(channelID, summary)
		}
		// Drop the buffer so the next turn starts a fresh message instead of
		// editing this turn's final message (the line buffer otherwise carries
		// continuation state across the turn boundary).
		c.resetBuffer(channelID)
	}
}

func (c *DiscordChannel) rendererFor(channelID string) *Renderer {
	c.rendererMu.Lock()
	defer c.rendererMu.Unlock()
	r, ok := c.renderers[channelID]
	if !ok {
		r = NewRenderer(c, channelID)
		c.renderers[channelID] = r
	}
	return r
}

// unwrapBacktickCommand strips a single surrounding pair of backticks from
// content when the result is a single-line slash command. This lets a user type
// `/clear` (backticked, so Discord won't hijack it as a native slash command)
// and still have the router recognise it as a command. Any other text — plain
// messages, code spans that aren't commands, code fences, multi-line input — is
// returned unchanged.
func unwrapBacktickCommand(content string) string {
	trimmed := strings.TrimSpace(content)
	if strings.ContainsRune(trimmed, '\n') {
		return content
	}
	if len(trimmed) < 2 || trimmed[0] != '`' || trimmed[len(trimmed)-1] != '`' {
		return content
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if !strings.HasPrefix(inner, "/") {
		return content
	}
	return inner
}

func (c *DiscordChannel) handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore other bots, but accept webhook-posted messages: webhooks are a
	// first-class way to inject channel input (used by the e2e test driver) and
	// are distinguishable from real bots by a non-empty WebhookID.
	if m.Author.Bot && m.WebhookID == "" {
		return
	}
	channelID := m.ChannelID

	// Build message content from text and attachments. Unwrap a single-line
	// command wrapped in backticks (e.g. `/clear`) so users can escape Discord's
	// slash-command autocomplete and still have it treated as a command.
	content := unwrapBacktickCommand(m.Content)
	var images []types.ImageData

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

	// Ignore empty messages (no text, no valid attachments, no images).
	if strings.TrimSpace(content) == "" && len(images) == 0 {
		return
	}

	id, err := c.resolveConversation(channelID)
	if err != nil {
		log.Printf("discord: resolve conversation for channel %s: %v", channelID, err)
		c.sendSystem(channelID, "⚠️ "+err.Error())
		return
	}

	prompt := buildPrompt(content, images)

	// Sending only fires the prompt now (it no longer blocks for the turn), so the
	// typing indicator is stopped when the turn completes — on the PromptResponse
	// handled in Receive — not here. On a failure there will be no such response,
	// so stop it explicitly.
	go func() {
		c.typing.Start(channelID)
		if err := c.submitPrompt(id, prompt); err != nil {
			log.Printf("discord: submit to conversation %v: %v", id, err)
			c.sendSystem(channelID, "⚠️ "+err.Error())
			c.typing.Stop(channelID)
		}
	}()
}

// submitPrompt routes a built prompt to a conversation: a leading-slash message
// is interpreted as a command, otherwise the prompt is fired (after the session
// finishes initializing) via the router's generic Send, which fans the raw
// PromptRequest out to subscribers for persistence/echo. It does not block for
// the turn; completion arrives later as a PromptResponse in Receive.
func (c *DiscordChannel) submitPrompt(id types.ConversationMeta, prompt []acp.ContentBlock) error {
	// A leading-slash message is a command, not a prompt. Only the text block
	// matters for that check; any image blocks ride along untouched in the
	// PromptRequest sent below.
	if len(prompt) > 0 && prompt[0].Text != nil {
		handled, err := c.router.HandleCommands(c.ctx, id, prompt[0].Text.Text)
		if err != nil || handled {
			return err
		}
	}
	meta, err := c.router.WaitReady(c.ctx, id)
	if err != nil {
		return err
	}
	return c.router.Send(c.ctx, meta, acp.PromptRequest{
		SessionId: meta.SessionID,
		Prompt:    prompt,
	})
}

// resolveConversation returns the conversation backing a Discord channel,
// creating it on first use. The channel's name is resolved to a working
// directory by searching searchPaths, and is also used as the project ID.
func (c *DiscordChannel) resolveConversation(channelID string) (types.ConversationMeta, error) {
	c.mu.Lock()
	if id, ok := c.convByChannel[channelID]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	ch, err := c.session.Channel(channelID)
	if err != nil {
		return types.ConversationMeta{}, errors.Wrap(err, "looking up channel")
	}
	name := ch.Name

	cwd, ok := c.findProjectDir(name)
	if !ok {
		return types.ConversationMeta{}, errors.Errorf("no project directory named %q found in search paths", name)
	}

	// Agent, sandbox and hooks are resolved centrally from the project's
	// .acpp.yaml by Router.Create; c.agent is the bot default it falls back to.
	opts := types.SessionOpts{
		ProjectID: name,
		Agent:     c.agent,
		CWD:       cwd,
		Source:    "discord",
	}

	id, err := c.router.Create(c.ctx, opts)
	if err != nil {
		return types.ConversationMeta{}, errors.Wrap(err, "creating conversation")
	}

	c.mu.Lock()
	c.convByChannel[channelID] = id
	c.channelByProject[id.ProjectID] = channelID
	c.mu.Unlock()
	return id, nil
}

// findProjectDir searches searchPaths for a directory named name and returns its
// absolute path.
func (c *DiscordChannel) findProjectDir(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	for _, base := range c.searchPaths {
		candidate := filepath.Join(base, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// buildPrompt assembles ACP content blocks from message text and image attachments.
func buildPrompt(content string, images []types.ImageData) []acp.ContentBlock {
	var blocks []acp.ContentBlock
	if strings.TrimSpace(content) != "" {
		blocks = append(blocks, acp.TextBlock(content))
	}
	for _, img := range images {
		blocks = append(blocks, acp.ImageBlock(base64.StdEncoding.EncodeToString(img.Data), img.MimeType))
	}
	return blocks
}

// handleReady logs when the bot connects and clears any stale global slash
// commands once. Commands are issued as plain text messages (handled by
// router.HandleCommands), so the bot registers none; clearing removes commands
// left registered by earlier versions of the bot that would otherwise be
// hijacked into the un-handled interaction path.
func (c *DiscordChannel) handleReady(s *discordgo.Session, r *discordgo.Ready) {
	log.Printf("Discord bot connected as %s#%s", r.User.Username, r.User.Discriminator)
	c.commandsOnce.Do(func() {
		if _, err := s.ApplicationCommandBulkOverwrite(r.User.ID, "", nil); err != nil { // "" = global
			log.Printf("discord: clear stale slash commands: %v", err)
			return
		}
		log.Printf("discord: cleared stale global slash commands")
	})
}

// Close shuts the Discord session down.
func (c *DiscordChannel) Close() error {
	return c.session.Close()
}

// ---- RenderSink implementation (source == Discord channel ID) ----

func (c *DiscordChannel) SendTextFragment(source string, s string) error {
	return c.GetBufferedWriter(source).Write(s)
}

func (c *DiscordChannel) SendThoughtFragment(source string, s string) error {
	return c.SendTextFragment(source, s)
}

func (c *DiscordChannel) SendImage(source string, mimeType string, data []byte) error {
	if err := c.GetBufferedWriter(source).Flush(); err != nil {
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
	_, err := c.session.ChannelFileSend(source, "image"+ext, bytes.NewReader(data))
	c.typing.Refresh(source)
	return err
}

func (c *DiscordChannel) SendToolUsage(source string, toolCallID string) ToolUsageUpdater {
	var t ToolUsage
	var msgID string
	return func(toolUsage ToolUsage) error {
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

		if msgID == "" {
			msg, err := c.session.ChannelMessageSend(source, content)
			if err != nil {
				return err
			}
			msgID = msg.ID
			c.typing.Refresh(source)
		} else {
			if _, err := c.session.ChannelMessageEdit(source, msgID, content); err != nil {
				return err
			}
		}
		return nil
	}
}

func (c *DiscordChannel) SendPlanUpdate(source string, plan PlanUpdate) error {
	if err := c.GetBufferedWriter(source).Flush(); err != nil {
		return errors.WithStack(err)
	}
	_, err := c.session.ChannelMessageSend(source, formatPlanUpdate(plan))
	if err != nil {
		return errors.WithStack(err)
	}
	c.typing.Refresh(source)
	return nil
}

// sendSystem posts server-generated text (errors, status) to a Discord channel.
func (c *DiscordChannel) sendSystem(source string, content string) {
	for _, chunk := range splitMessage(content) {
		if _, err := c.session.ChannelMessageSend(source, chunk); err != nil {
			log.Printf("discord: send system message to channel %s: %v", source, err)
			return
		}
	}
	c.typing.Refresh(source)
}

// resetBuffer drops the output buffer for a Discord channel. The next write
// recreates it with fresh continuation state (currentLine/hasOutput) and a fresh
// lastMsgID, so a new turn always starts a new message rather than editing the
// previous turn's last message.
func (c *DiscordChannel) resetBuffer(source string) {
	c.bufferMu.Lock()
	defer c.bufferMu.Unlock()
	delete(c.buffers, source)
}

// GetBufferedWriter returns the code-block-aware output buffer for a Discord
// channel, creating it on first use.
func (c *DiscordChannel) GetBufferedWriter(source string) *helpers.BlockBuffer {
	c.bufferMu.Lock()
	defer c.bufferMu.Unlock()

	buffer, exists := c.buffers[source]
	if !exists {
		var lastMsgID string
		buffer = helpers.NewBlockBuffer(func(line string, update bool) error {
			line = stripCodeBlockLanguage(line)
			chunks := splitMessage(line)
			if update && lastMsgID != "" && len(chunks) == 1 {
				_, err := c.session.ChannelMessageEdit(source, lastMsgID, chunks[0])
				return err
			}
			for i, chunk := range chunks {
				if i == 0 && update && lastMsgID != "" {
					if _, err := c.session.ChannelMessageEdit(source, lastMsgID, chunk); err != nil {
						return err
					}
				} else {
					resp, err := c.session.ChannelMessageSend(source, chunk)
					if err != nil {
						return errors.WithStack(err)
					}
					lastMsgID = resp.ID
					c.typing.Refresh(source)
				}
			}
			return nil
		})

		// Render markdown tables as embeds when possible.
		buffer.SetTableOutput(func(lines []string) error {
			table := helpers.ParseMarkdownTable(lines)
			if table == nil {
				content := "```\n" + strings.Join(lines, "\n") + "\n```"
				_, err := c.session.ChannelMessageSend(source, content)
				if err != nil {
					return errors.WithStack(err)
				}
				c.typing.Refresh(source)
				return nil
			}
			if table.CanRenderAsEmbed() {
				if _, err := c.session.ChannelMessageSendEmbed(source, table.ToEmbed()); err != nil {
					return errors.WithStack(err)
				}
				c.typing.Refresh(source)
				return nil
			}
			if _, err := c.session.ChannelMessageSend(source, table.ToCodeBlock()); err != nil {
				return errors.WithStack(err)
			}
			c.typing.Refresh(source)
			return nil
		})

		c.buffers[source] = buffer
	}
	return buffer
}

// ---- attachment helpers ----

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
func downloadImageAttachment(url string, contentType string) (types.ImageData, error) {
	resp, err := http.Get(url)
	if err != nil {
		return types.ImageData{}, errors.Wrap(err, "failed to download image")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return types.ImageData{}, errors.Errorf("download image: status %d", resp.StatusCode)
	}

	const maxSize = 20 * 1024 * 1024 // 20MB limit
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
	if err != nil {
		return types.ImageData{}, errors.Wrap(err, "failed to read image body")
	}

	// Detect actual MIME type from bytes — Discord may report wrong content type
	// (e.g., image/webp for a PNG), which causes API rejections.
	detected := http.DetectContentType(data)
	if isImageAttachment(detected) {
		contentType = detected
	}

	return types.ImageData{Data: data, MimeType: contentType}, nil
}
