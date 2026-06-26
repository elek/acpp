# Web Channel Refactor — Implementation Plan

Based on: `docs/plans/2026-04-18-web-channel-refactor-design.md`

## Step 1 — Create `web/channel.go` with WebChannel

Implement `channel.Channel`:
- struct with `hub *Hub`, `store db.SessionReader`, `input chan channel.Input`, `closed atomic.Bool`
- constructor `NewWebChannel(hub, store)`
- `PushInput(Input)` — non-blocking send, drop if closed
- `SendTextFragment/SendThoughtFragment/SendTextMessage` → publish logEntry with event types `agent_message_chunk`, `agent_thought_chunk`, `text_message`
- `SendToolUsage` → publish `tool_call`; returned updater publishes `tool_call_update`
- `SendPlanUpdate` → publish `plan`
- `StartConversation/FinishConversation` → publish `prompt` / `prompt_finished`
- `GetTopic/GetChannelName` → look up from store, fallback to sessionID
- `SendImage/SendFile` → publish `image`/`file`
- `AskYesNo` → return `errors.New("not supported in web")`
- `Input()` returns receive-only chan
- `Close()` closes input chan

Match existing payload shapes from `channel/gateway.go:marshalEvent` so the frontend sees identical events.

**Verify**: `go build ./...`

## Step 2 — Register WebChannel in Gateway + rewire StartSessionWeb

In `web.New(...)`, create a `WebChannel` and expose via `Server.WebChannel()` getter.

In CLI (`cli/console.go`, `cli/discord.go`), register the web channel:
```go
webCh := srv.WebChannel()
gw.ChannelManager().Register("web", func(ctx context.Context) (channel.Channel, error) {
    return webCh, nil
})
```
Call `gw.StartChannel(ctx, "web")` (or equivalent) so `startChannelListener` runs.

Check existing CLI for the channel-registration pattern and mirror it.

Rewrite `Gateway.StartSessionWeb`:
- Build `cs := ChannelSource{ChannelID: "web", SourceID: SourceID(sessionID)}`
- Use `gw.makeBroadcast(sessionID)` instead of noop
- Call `gw.Subscribe(sessionID, cs)` after storing the relay
- Delete the inline `onUpdate` DB+publisher logic — `makeOnUpdate` (or equivalent) already handles it; check and unify.

**Verify**: `go build ./...` + `go test ./channel/...`

## Step 3 — Refactor Server.sendPrompt to push via WebChannel

In `web/server.go`:
- Delete `PromptSender`, `CommandHandler` interfaces + fields + `WithPrompter`, `WithCommands`
- Delete `webCommands`, `isWebCommand`
- Rewrite `sendPrompt` to just push an Input:
  ```go
  s.webChannel.PushInput(channel.Input{
      Source: channel.SourceID(id),
      Message: body.Prompt,
  })
  return c.JSON(http.StatusAccepted, map[string]string{"status":"accepted"})
  ```

In `web/projects.go`:
- Replace `s.prompter.SendPromptBySessionID(sessionID, body.Prompt)` with `s.webChannel.PushInput(...)`.

Update `cli/console.go` and `cli/discord.go`:
- Remove `.WithPrompter(gw).WithCommands(gw)` calls.

**Verify**: `go build ./...`

## Step 4 — Delete dead methods in Gateway

In `channel/gateway.go`:
- Delete `SendPromptBySessionID` (gateway.go:1433+)
- Delete `HandleCommandBySessionID` (gateway.go:1353–1429)

**Verify**: `go build ./...`

## Step 5 — session_replaced event in handleClear

In `Gateway.handleClear`:
- After creating the new session, if `gw.publisher != nil`:
  ```go
  payload, _ := json.Marshal(map[string]string{"new_session_id": newSessionID})
  gw.publisher.PublishEvent(oldSessionID, "session_replaced", payload)
  ```

**Verify**: `go build ./...` + `go test ./channel/...`

## Step 6 — Frontend event handlers

In `web/templates/session.html` and `web/templates/projectview.html`:
- Add `case 'session_replaced': window.location.href = '/session/' + payload.new_session_id; break;`
- Add `case 'text_message':` to render a server message bubble.

**Verify**: Manual — load web, run `/get`, `/status`, `/clear`, confirm UI updates.

## Step 7 — Integration test

Add `channel/gateway_test.go` (or `web/channel_test.go`) stub test:
- Create a Gateway with a stub WebChannel
- Push `/get` as Input
- Assert `SendTextMessage` was called on the stub

**Verify**: `go test ./...`

## Files

- `web/channel.go` (new)
- `web/server.go` (delete interfaces, simplify)
- `web/projects.go` (pushInput for initial prompt)
- `web/hub.go` (maybe expose helper for logEntry construction)
- `web/templates/session.html`, `web/templates/projectview.html`
- `channel/gateway.go` (delete 2 methods, unify StartSessionWeb, add session_replaced)
- `cli/console.go`, `cli/discord.go` (register web channel, drop WithPrompter/WithCommands)

