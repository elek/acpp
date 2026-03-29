package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/coder/acp-go-sdk"
	"github.com/elek/acpp/channel"
	"github.com/elek/acpp/console"
	"github.com/elek/acpp/discord"
	"github.com/pkg/errors"
)

type Replay struct {
	DiscordToken   string `env:"DISCORD_TOKEN" help:"Discord bot token (uses Discord channel if set, otherwise console)"`
	DiscordChannel string `env:"DISCORD_CHANNEL" help:"Discord channel ID to send messages to (required when using Discord)"`
}

func (r *Replay) Run(kctx *kong.Context) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Choose channel based on DISCORD_TOKEN env var
	var ch channel.Channel
	var source channel.SourceID
	if r.DiscordToken != "" && r.DiscordChannel != "" {
		createChannel := discord.NewDiscordChannel(r.DiscordToken)
		var err error
		ch, err = createChannel(ctx)
		if err != nil {
			return errors.WithStack(err)
		}
		source = channel.SourceID(r.DiscordChannel)
	} else {
		var err error
		ch, err = console.CreateChannel(ctx)
		if err != nil {
			return errors.WithStack(err)
		}
		source = channel.SourceID("console")
	}

	// Create relay for replay
	relay := channel.NewRelayForReplay(source, ch)

	// Read JSONL from stdin
	scanner := bufio.NewScanner(os.Stdin)
	// Increase buffer size for potentially large JSON lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var update acp.SessionUpdate
		if err := json.Unmarshal([]byte(line), &update); err != nil {
			// Skip malformed lines silently
			continue
		}

		relay.HandleUpdate(update)
	}

	// Flush any remaining buffered content via channel's Stop
	ch.FinishConversation(source, acp.PromptResponse{})

	if err := scanner.Err(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
