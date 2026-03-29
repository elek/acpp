# Gateway Refactor Design

## Problem
Bot mixes session management, channel management, and message routing into one struct.
Relay couples session-to-channel translation with session ownership.
No support for many-to-many session-to-channel relationships.

## New Architecture

### Types
- `ChannelID` — identifies a Channel instance ("discord-bot-1", "console")
- `SourceID` — identifies an endpoint within a Channel (Discord channel ID)
- `ChannelSource{ChannelID, SourceID}` — uniquely identifies an external endpoint
- Replaces the existing `Source` type

### SessionManager (acp/)
Registry for session lifecycle. Create/Get/Close/All.
No knowledge of channels, DB, or routing.

### ChannelManager (channel/)
Registry for Channel instances. Register/Get/Remove/All.
Channels created externally and registered here.

### Gateway (channel/)
Central router replacing Bot + Relay's routing role.

Many-to-many maps:
- sessionToSources: sessionID -> []ChannelSource
- sourceToSessions: ChannelSource -> []sessionID
- activeSession: ChannelSource -> sessionID (for prompt routing)

Key behaviors:
- Start(ctx): consume Input() from all channels, start idle checker
- Subscribe/Unsubscribe: manage session-to-channel links
- StartSession: create session, relay, subscribe, persist to DB
- CloseSession: unsubscribe all, close session, persist to DB
- Input routing: message from ChannelSource -> activeSession -> Prompt()
- Update routing: session update -> all subscribed ChannelSources
- Command handling: /start, /stop, /status, etc.

### Relay changes
No longer owns (Source, Channel) pair.
Gets broadcast callback from Gateway.
Tool updaters keyed by ChannelSource (per-endpoint edit handles).
