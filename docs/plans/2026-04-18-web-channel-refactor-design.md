# Web UI as Channel — Refactor Design

Date: 2026-04-18

## Problem

Slash commands like `/get` don't work from the web UI. Root cause: the web UI has its own parallel command pipeline (`HandleCommandBySessionID`, `webCommands` list) that supports only a subset of commands (`/status`, `/clear`, `/cancel`, `/stop`). The full command set lives in `Gateway.OnCommandReceived` and is only reachable via the Channel-based input path used by Discord and Console.

## Goal

Unify command handling. The web UI implements the `Channel` interface; all user input — from Discord, Console, and web — flows through the same `Gateway.OnCommandReceived` / `OnMessageReceived` pipeline.

## Design

### SourceID model

`SourceID = sessionID`. The web Channel is stateless about sources: any SourceID passed to a `Send*` method is forwarded to `hub.Publish(sessionID, ...)`. Multiple browsers viewing the same session already work via the existing Hub fan-out.

### WebChannel implementation

New file `web/channel.go`. One `WebChannel` struct implementing `channel.Channel`, registered under `ChannelID = "web"` in the `ChannelManager`.

- `input chan channel.Input` — owned by the channel, exposed via `Input()`.
- `PushInput(Input)` — called by HTTP handlers to inject user input.
- `Send*` methods translate to `hub.Publish(sessionID, logEntry{...})` with event_type strings matching the existing frontend listeners (`agent_message_chunk`, `agent_thought_chunk`, `tool_call`, `tool_call_update`, `plan`, `prompt`, `prompt_finished`, plus new `text_message` for gateway-emitted messages).
- `AskYesNo` returns `errors.New("not supported in web")` (no permission flow yet).
- `SendImage` / `SendFile` publish `image` / `file` events the frontend can ignore for now.
- `GetTopic`, `GetChannelName` look up the session in the DB store and return project name / session ID.
- `Close()` closes the input chan.

### HTTP handler changes

`/api/session/:id/prompt`:
```go
webChannel.PushInput(channel.Input{
    Source: channel.SourceID(sessionID),
    Message: body.Prompt,
})
return c.JSON(http.StatusAccepted, map[string]string{"status": "accepted"})
```

No branching on `isWebCommand`. Gateway's `startChannelListener` does the `/`-prefix detection.

### Initial prompt on session creation

`StartSessionWeb` currently calls `SendPromptBySessionID` for the first prompt. Replace with `webChannel.PushInput(Input{Source: sessionID, Message: prompt})` **after** the session is subscribed to its web source.

### `/clear` — `session_replaced` event

`Gateway.handleClear` currently closes the session and creates a new one. For the web UI the browser needs the new session ID. Solution: `handleClear` unconditionally publishes a `session_replaced` event via `gw.publisher` with the new session ID. Discord ignores it (no listener); web frontend listens on its WebSocket and navigates.

No new Channel method is required.

### Subscription

When the web creates a session:
```go
gw.Subscribe(sessionID, ChannelSource{ChannelID: "web", SourceID: SourceID(sessionID)})
```

This ties the session to its source so `SendToSource`-based outputs reach the web Channel.

## Dead code removal

- Delete `Gateway.HandleCommandBySessionID`
- Delete `Gateway.SendPromptBySessionID`
- Delete `web.CommandHandler` and `web.PromptSender` interfaces + Server fields
- Delete `webCommands` slice and `isWebCommand` function in `web/server.go`
- Remove the command-check branch in `Server.sendPrompt`

## Per-command notes

- `/get`, `/set`, `/status`, `/pwd`, `/cd`, `/ls`, `/models`, `/modes`, `/mode` — plain text output, works unchanged via `SendToSource` → `WebChannel.SendTextMessage` → Hub.
- `/clear` — unified with `session_replaced` event (see above).
- `/cancel`, `/stop` — work unchanged.
- `/start` — left as-is; creating a second session on a web source is odd but not a blocker.
- `/exit` — left as-is; its effect on web is closing the session, which is fine.

## Frontend changes

Minimal. Add one WebSocket event handler for `session_replaced`:

```js
case 'session_replaced':
    window.location.href = '/session/' + payload.new_session_id;
    break;
```

Also add a handler for `text_message` to render server-generated messages inline.

## Files touched

- `web/channel.go` (new)
- `web/server.go` (delete interfaces, simplify `sendPrompt`, expose WebChannel)
- `web/projects.go` (initial-prompt path)
- `channel/gateway.go` (delete `HandleCommandBySessionID`, `SendPromptBySessionID`; emit `session_replaced` in `handleClear`)
- `web/templates/session.html`, `web/templates/projectview.html` (new event handlers)
- `cli/console.go` (and friends — wherever the CLI wires up web + gateway)

