package main

import (
	"github.com/alecthomas/kong"
	cli2 "github.com/elek/acpp/cli"
)

type CLI struct {
	//Console cli2.Console `cmd:"" help:"Start console based session management"`
	Discord cli2.Discord `cmd:"" help:"Start Discord bot for session management"`
	Replay  cli2.Replay  `cmd:"" help:"Replay ACP messages from stdin (JSONL) to test channels"`
	Cat    cli2.Cat    `cmd:"" help:"Send prompts from stdin to ACP agent, output JSONL"`
	Run    cli2.Run    `cmd:"" help:"Send prompt to ACP agent and display formatted response"`
	Web    cli2.Web    `cmd:"" help:"Start web UI for browsing and running sessions"`
	Serve  cli2.Serve  `cmd:"" help:"Start both the web UI and the Discord bot on a shared router"`
	Read cli2.Read `cmd:"" help:"Read a text file (used by sandbox delegation)"`
}

func main() {
	var app CLI
	ctx := kong.Parse(&app,
		kong.Name("acpp"),
		kong.Description("Agent Client Protocol Proxy & Utility"),
		kong.UsageOnError(),
	)
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}
