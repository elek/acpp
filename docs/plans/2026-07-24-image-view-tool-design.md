# image-view Tool Design

Date: 2026-07-24

## Goal

Expose a new tool, `image-view`, to ACP agents. The agent can call it with an
absolute file path **or** base64 image data. The tool itself is effectively a
NOOP (returns "ok"), but its purpose is to signal the web UI to display the
image inline in the conversation.

## Background

acpp is purely an ACP **client**: it consumes `tool_call` / `tool_call_update`
notifications and displays them. It does not currently define any tools the
agent can call — `NewSessionRequest.McpServers` is passed empty
(`router/router.go:433`). The only ACP mechanism for a client to expose callable
tools to the agent is an MCP server, and `acp.McpServer` supports **stdio only**
(command/args/env) — no HTTP transport (`acp/types.go:588`).

The codebase hand-rolls its own ACP JSON-RPC client rather than using an SDK, and
already uses the "binary invokes a subcommand on itself" pattern (`cli/read.go`
for sandbox delegation). A minimal hand-rolled MCP stdio server fits this style.

## Decisions

- **(Q1) Full path**: acpp ships a built-in MCP server exposing `image-view`,
  auto-injected into every session, plus the web rendering. Self-contained.
- **(Q2) Two optional named params**: `path`, `data`, plus optional `mimeType`.
- **(Q3) Base64 rides in the tool-call flow** (option B): no web file-read
  endpoint. The MCP server reads the path, base64-encodes it, and returns an MCP
  **image content block**; the agent forwards it as image `ToolCallContent`.
- **(Q4) Displayed both** inline in the session stream and on the tool detail page.
- **(Q5) Always on**: auto-injected, zero config. (Opt-out can be added later.)

## Architecture

### Part 1 — the `image-view` MCP tool (backend)

**New subcommand `acpp mcp` (`cli/mcp.go`).** A minimal stdio MCP server: a
JSON-RPC loop over stdin/stdout handling three methods.

- `initialize` → advertise protocol version + `{ tools: {} }` capability.
- `tools/list` → return one tool:
  - name: `image-view`
  - description: "Display an image to the user in the web UI. Provide an absolute
    file path OR base64 data."
  - inputSchema: `{ path?: string, data?: string, mimeType?: string }`
- `tools/call` for `image-view`:
  - if `path` set: read the file, base64-encode, sniff mime via
    `http.DetectContentType`.
  - if `data` set: use as-is (with `mimeType`, default sniff/`image/png`).
  - return an MCP result containing an **image content block**
    `{ type: "image", data: <base64>, mimeType }` plus a short `"ok"` text.
  - on error (missing/unreadable file, not an image): return an MCP error result
    so the agent sees the failure; nothing else breaks.

**Injection (`router/router.go:433`).** Replace the empty `McpServers` with one
entry pointing the agent at this same binary:

```go
McpServers: []acp.McpServer{{
    Name:    "acpp",
    Command: <os.Executable()>,
    Args:    []string{"mcp"},
}}
```

`claude-code-acp` spawns `acpp mcp` as a child, discovers `image-view`, and can
call it. The call is reported back as a normal `tool_call` / `tool_call_update`
with image content — title `mcp__acpp__image-view` (already recognized as an
`mcp__` tool by the web).

### Part 2 — web rendering + data flow

**Data flow (no router/persistence changes).** The image arrives as a
`ToolCallContentContent` wrapping an `image` `ContentBlock`, and flows unchanged:
agent → `router.Receive` → `WebChannel.Receive` → `db.MarshalEvent` → hub →
websocket/replay, persisted as a `tool_call_update` log row. Base64 rides in the
existing event payload.

**Inline stream (`session.html` + `projectview.html`, both edited).**
`formatToolUsage` today extracts only text from tool content. Add: when a tool
call's merged content contains an `image` block, render
`<img src="data:{mimeType};base64,{data}">` inside the tool card (capped
width/height thumbnail, click-to-open). Both templates duplicate the `toolCalls`
accumulation + rendering, so both get the change.

**Detail page (`tool.html`).** `renderToolCallContent` handles
`content`(text)/`diff`/`terminal` and falls back to raw JSON for images. Add an
`image` case rendering the full-size image.

## Verification (TDD)

The one real assumption is *"claude-code-acp forwards MCP image results as image
content blocks."* Verify concretely:

1. **Unit test the `acpp mcp` server**: feed it `initialize` / `tools/list` /
   `tools/call` JSON-RPC; assert the image content block out (path mode, base64
   mode, error cases).
2. **Integration/e2e**: a session where the agent calls `image-view`; assert an
   image renders in the web stream. If forwarding differs from expectation,
   discover it here and adjust (fallback: surface base64 via `rawInput`, or
   reconsider a web file-read endpoint).

## Out of scope (YAGNI)

- No config toggle / opt-out (add later if needed).
- No HTTP MCP transport.
- No web file-read endpoint.
- No server-side image resizing/caching.

## Key references

- `router/router.go:433` — where `McpServers` is injected.
- `acp/types.go:588` — `McpServer` (stdio only).
- `acp/types.go:126` — `ContentBlockImage` (base64 `Data` + `MimeType`).
- `acp/types.go:251` — `ToolCallContent` union.
- `web/channel.go:82` — `WebChannel.Receive` → `db.MarshalEvent`.
- `db/store.go:806` — `MarshalEvent` / `ClassifyEvent`.
- `web/templates/session.html` — `formatToolUsage`, `toolCalls` accumulation.
- `web/templates/projectview.html` — duplicated tool logic.
- `web/templates/tool.html` — `renderToolCallContent`.
- `cli/read.go` — precedent for a binary self-invocation subcommand.
