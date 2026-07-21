package main

import (
	"github.com/alecthomas/kong"
	cli2 "github.com/elek/acpp/cli"
)

type CLI struct {
	Replay cli2.Replay `cmd:"" help:"Replay ACP messages from stdin (JSONL) to test channels"`
	Cat    cli2.Cat    `cmd:"" help:"Send prompts from stdin to ACP agent, output JSONL"`
	Run    cli2.Run    `cmd:"" help:"Send prompt to ACP agent and display formatted response"`
	Serve  cli2.Serve  `cmd:"" help:"Start the web UI, scheduler, and (with a token) the Discord bot on a shared router"`
	Read   cli2.Read   `cmd:"" help:"Read a text file (used by sandbox delegation)"`
	Tck    cli2.Tck    `cmd:"" help:"Test ACP agent binaries and report a compatibility matrix"`
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
