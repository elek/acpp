package discord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/elek/acpp/router"
	"github.com/stretchr/testify/require"
)

// TestTextCommands_E2E drives the Discord text-command flow end to end against a
// real Discord channel and a real claude-code-acp agent.
//
// Slash commands cannot be invoked programmatically (Discord exposes no API to
// trigger an application command, and automating a user token violates ToS), so
// commands are plain text messages and we inject input via a Discord webhook —
// the bot accepts webhook-posted messages (see handleMessage). The webhook
// message reaches the bot as a normal MessageCreate, exactly like a human's.
//
// Requirements (the test skips unless all are present):
//   - DISCORD_TEST_BOT_TOKEN: token of the bot under test
//   - DISCORD_TEST_CHANNEL_ID: a channel the bot can read, send to, and in which
//     it has the "Manage Webhooks" permission
//   - claude-code-acp installed at the path below (matches the other agent tests)
func TestTextCommands_E2E(t *testing.T) {
	const bin = "/home/elek/.npm-global/bin/claude-code-acp"

	token := os.Getenv("DISCORD_TEST_BOT_TOKEN")
	channelID := os.Getenv("DISCORD_TEST_CHANNEL_ID")
	if token == "" || channelID == "" {
		t.Skip("set DISCORD_TEST_BOT_TOKEN and DISCORD_TEST_CHANNEL_ID to run")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("agent binary not available: %v", err)
	}

	rt := router.New()
	defer rt.Close()

	bot, err := NewDiscordChannel(token, bin, nil, rt)
	require.NoError(t, err)
	defer bot.Close()

	// Wait for the gateway READY so the bot's identity is known.
	require.Eventually(t, func() bool {
		return bot.session.State.User != nil && bot.session.State.User.ID != ""
	}, 15*time.Second, 200*time.Millisecond, "bot did not become ready")
	botID := bot.session.State.User.ID

	// resolveConversation maps the channel's *name* to a project dir under
	// searchPaths; point it at a temp dir holding a subdir of that name so the
	// agent has a working directory.
	ch, err := bot.session.Channel(channelID)
	require.NoError(t, err)
	tmp := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(tmp, ch.Name), 0o755))
	bot.searchPaths = []string{tmp}

	// A webhook lets the test post channel input that the bot will process.
	wh, err := bot.session.WebhookCreate(channelID, "acpp-e2e", "")
	require.NoError(t, err)
	defer func() { _ = bot.session.WebhookDelete(wh.ID) }()

	// send posts content via the webhook and returns its message ID, used as the
	// "after" anchor when scanning for the bot's reply.
	send := func(content string) string {
		m, err := bot.session.WebhookExecute(wh.ID, wh.Token, true, &discordgo.WebhookParams{Content: content})
		require.NoError(t, err)
		return m.ID
	}

	// waitForBotReply polls for a bot-authored (non-webhook) message posted after
	// `after` whose content satisfies pred.
	waitForBotReply := func(after string, pred func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			msgs, err := bot.session.ChannelMessages(channelID, 50, "", after, "")
			require.NoError(t, err)
			for _, m := range msgs { // newest-first; any match is enough
				if m.Author != nil && m.Author.ID == botID && m.WebhookID == "" && pred(m.Content) {
					return
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("timed out waiting for matching bot reply after %s", after)
	}

	// 1. Prompt round trip: the agent answers a question in this channel.
	a1 := send("Reply with exactly one word: the capital of Spain.")
	waitForBotReply(a1, func(s string) bool { return strings.Contains(s, "Madrid") })

	// 2. /clear restarts the session: HandleCommands -> Restart emits
	//    ConversationReplaced, which the bot renders as "session restarted".
	a2 := send("/clear")
	waitForBotReply(a2, func(s string) bool { return strings.Contains(s, "session restarted") })
}
