# ACPP — Agent Client Protocol Proxy

ACPP is a multi-channel proxy for AI coding agents. It bridges agent protocols (ACP subprocess, OpenCode HTTP) to communication channels (Discord, Console, Web UI), providing session management, usage tracking, and persistent logging.

![ACPP Screenshot](screenshot.png)

## Status

** IMPORTANT **

 1. Code is published in __snapshot source-available__ mode, the Git history is not shared. Iternal Git history represents the snapshots of agentic development and not shared.
 2. This code is not indented to be a production-ready code. If you (or your assistant) can read code: you are welcome. If not, this is probably not for you. I use it as a daily driver, but it's not a plan to make a generic product from it.


## Installation

```bash
go install github.com/elek/acpp@latest
```

Or build from source:

```bash
go build -o acpp .
```

## Quick Start

### Console Mode

Interactive terminal session with an ACP agent:

```bash
acpp console
```

### Discord Bot

Run as a Discord bot (requires token):

```bash
export DISCORD_TOKEN="your-token"
acpp discord
```

### Web UI

Browse sessions and view live logs:

```bash
acpp web --addr :8080
```

## CLI Commands

| Command | Description |
|---------|-------------|
| `acpp console` | Interactive terminal session |
| `acpp discord` | Discord bot |
| `acpp web` | Web UI for browsing sessions |
| `acpp replay` | Replay ACP JSONL events from stdin |
| `acpp cat <agent>` | Pipe prompts from stdin, output JSONL |

## Session Commands

These commands work across all channels (Discord, Console, Web):

| Command | Description |
|---------|-------------|
| `/start` | Create a new session |
| `/stop` | Stop the active session |
| `/status` | Show session info and usage |
| `/cancel` | Cancel the current operation |
| `/clear` | Restart session with same config |
| `/exit` | Shutdown the application |
| `/modes` | List available agent modes |
| `/mode <id>` | Switch agent mode |
| `/pwd` | Show working directory |
| `/cd <dir>` | Change directory (restarts session) |
| `/ls [dir]` | List directory contents |
| `!<cmd>` | Execute a shell command |

## Internal model

 * Session: one client may use more sessions.
 * Client an ACP client connection. Tight to an ACP session. 
 * Conversation: a conversation from an agentic loop (ACPSession + usage metadata)
 * Project: collection of one or more sessions (usually tight to a single GitHub repo)
 * Process: a running ACP agent instance (stdin, stdout)

## Agent Backends

ACPP supports two agent backends, selected by the agent name:

- **ACP** (default) — spawns the agent as a subprocess, communicates via stdin/stdout using the [Agent Client Protocol](https://github.com/coder/acp-go-sdk). Default agent: `claude-code-acp`.
- **OpenCode** — connects to an [OpenCode](https://opencode.ai) HTTP server via REST + SSE. Prefix the agent name with `@opencode`.

## Configuration

Config file: `~/.config/acpp/config.yaml` (or `$XDG_CONFIG_HOME/acpp/config.yaml`)

```yaml
# PostgreSQL connection for session persistence
database:
  dsn: "postgres://localhost/acpp?sslmode=disable"

# Discord bot token (alternative to DISCORD_TOKEN env var)
discord_token: "..."

# Web UI listen address
web_addr: ":8080"

# Default session parameters
defaults:
  agent: "claude-code-acp"
  sandbox: "bwrap.sh"
  env_whitelist:
    - PATH
    - HOME
    - ANTHROPIC_API_KEY

# Resolve project directories from channel name
search_path:
  - /home/user/projects

# Directory containing sandbox scripts
sandbox_dir: /opt/sandboxes

# Tool permission rules
tool_permissions:
  - kind: "execute"
    action: "ask"          # "ask" or "deny"
    contains: ["rm", "sudo"]

# OpenTelemetry OTLP exporter
otlp:
  endpoint: "localhost:4317"
  tls:
    insecure: true
```

### Discord Channel Topics

Configure per-channel session parameters in the Discord channel topic:

```
Agent: claude-code-acp
Dir: /home/user/myproject
Sandbox: bwrap.sh
Env: API_KEY=secret
```

### Directory Auto-Detection

When `search_path` is configured, ACPP maps Discord channel names to project directories automatically. A channel named `myproject` will use `/home/user/projects/myproject` if it exists.

## Database

ACPP uses PostgreSQL for session persistence and event logging. Migrations run automatically on startup via [goose](https://github.com/pressly/goose).

The database is optional — without it, sessions are ephemeral.

### Schema

- **session** — tracks agent, directory, status, token usage, cost, and timing
- **log** — stores all session events (prompts, responses, tool calls) as JSONB

## Monitoring

- **Prometheus metrics** exposed on `:9090` by default
- **OpenTelemetry OTLP** export via the `otlp` config section
- **Web UI** with live WebSocket streaming of session events

## Desktop App

The `desktop/` directory contains a [Wails](https://wails.io/)-based desktop UI wrapper.

### Build Dependencies

Install the following packages on the host before building:

- `pkg-config`
- `gtk3` (development headers)
- `webkitgtk 4.1` (development headers)
- `glib` (development headers)
- `wails` CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)

On Arch Linux:

```bash
pacman -S pkg-config gtk3 webkit2gtk-4.1 glib2
```

On Debian/Ubuntu:

```bash
apt install pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev libglib2.0-dev
```

### Test Script

`desktop/test.sh` builds the app, launches it, waits for the window to appear, and takes a screenshot. Additional runtime dependencies for the test: `xdotool`, `imagemagick` (for the `import` command).

## License

See [LICENSE](LICENSE) for details.
