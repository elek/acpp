# ACPP - Agent Client Protocol Proxy

## What This Is

Go application bridging AI agents (Claude Code ACP) with communication platforms. Implements ACP protocol for agent execution with HTTP API and Discord integration.

ACP (Agent Client Protocol) is defined at https://agentclientprotocol.com/

## Language

The project uses golang language. 

IMPORTANT: always use gopls / LSP / MCP server calls when possible to understand the codebase better, before work.

## Architecture

```
CLI (kong) → Commands → Session Management → ACP Client → Agent Subprocess
                              ↓
                    HTTP Server (chi) ←→ SSE Streaming
                              ↓
                    Discord Bot (discordgo) ←→ Message Relay
```

## Key Packages

- `internal/cli/` - Command handlers (run, serve, session)
- `internal/acp/` - ACP protocol client implementations
- `internal/session/` - Session state and thread-safe store
- `internal/server/` - HTTP API with SSE streaming
- `internal/discord/` - Bot, commands, message relay

## Patterns Used

- Subscriber pattern for multi-client event delivery
- Channel-based streaming with buffered events
- Context propagation with graceful shutdown
- sync.Map for thread-safe session storage

## Development

### Building & Running

```bash
go build -o acpp .
./acpp run "prompt"           # One-shot execution
./acpp serve                  # HTTP server (localhost:8080)
./acpp serve --discord        # With Discord bot (needs DISCORD_TOKEN)
```

### Testing

```bash
go test ./...
```

### Key Files to Understand

- `internal/session/session.go` - Core session lifecycle
- `internal/acp/client.go` - Two client types: StreamingClient (one-shot), SessionClient (persistent)
- `internal/discord/relay.go` - Message buffering and Discord delivery

### Adding New Features

- New CLI commands: Add to `internal/cli/`, register in `main.go`
- New API endpoints: Add handler in `internal/server/handlers.go`, route in `server.go`
- New Discord commands: Add in `internal/discord/commands.go`

### Concurrency Notes

- Sessions use channels for event streaming - don't block receivers
- Store uses sync.Map - safe for concurrent access
- Relay has mutex for buffer access - always lock before modifying

## Commit message rules

Each commit message must have an emoji at the beginning.

IMPORTANT: Doesn't use commit skill for this repository.

## Configuration

### Environment Variables

- `DISCORD_TOKEN` - Required for Discord bot mode

### CLI Flags

- `--host` / `--port` - Server bind address (default: localhost:8080)
- `--agent` - Agent command to run (default: claude-code-acp)
- `--verbose` - Discord output verbosity level

### Working Directory

Agent executes in current working directory. Session inherits the directory from where `acpp serve` was started.
