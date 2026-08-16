# About — TamizChat Server

## For the repository's About box

*Short, for the sidebar description (under 350 characters):*

> Self-hosted voice and text chat server in Go. Rooms, roles and moderation,
> files, a shared paint board, voice/video via LiveKit, and music bots — one
> static binary, SQLite, and an interactive admin panel instead of config files.

*Suggested topics:*

`go` · `golang` · `chat-server` · `voice-chat` · `websocket` · `self-hosted` ·
`livekit` · `webrtc` · `sqlite` · `discord-alternative` · `teamspeak-alternative`

---

## The longer version

TamizChat is a self-hosted alternative to Discord and TeamSpeak for a group that
would rather run its own server than be a guest on someone else's.

The server is a single Go binary. It has no `.env`, no external database, no
Redis and no config file to edit: settings live in SQLite and are edited from an
interactive admin panel that the same binary opens when run with no arguments.
Changes apply to the running server immediately — the panel asks "apply now?"
after each one.

**What it does**

- Rooms with passwords, capacities and a minimum role to enter
- Text chat, stickers, and file and image transfer
- A shared paint board, streamed stroke by stroke
- A 17-permission role system with a priority rule, bans, mutes and a moderation log
- Voice, video and screen sharing through LiveKit, with each user's rights in
  LiveKit mirroring their permissions here
- Music bots that join a room as participants of their own and play playlists
  uploaded from the client

**Decisions worth knowing before reading the code**

- **Room content is deliberately temporary.** Messages, files and drawings live
  in memory and are erased when the last person leaves. What is durable — rooms,
  roles, bans, bots, settings — is in SQLite.
- **The server never touches media.** It signs LiveKit tokens and enforces
  moderation; audio and video go around it. Even a music bot's audio is
  published as pre-encoded Opus packets, so the server decodes nothing.
- **No CGo anywhere.** SQLite is `modernc.org/sqlite`, so the binary is static
  and cross-compiles to four platforms with no libraries to match.
- **The identity is a client-generated UUID.** There are no accounts and no
  passwords per user; a server password, if set, is the only gate.

**Status.** Feature-complete and tested — 233 tests covering the protocol,
permissions, rate limits, uploads, the paint board, media and the bots, including
hostile input that must be answered rather than crash the server.

Built with [Claude Code](https://claude.com/claude-code).
