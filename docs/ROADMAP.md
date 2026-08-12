# TamizChat backend roadmap

Each phase is an independent, testable unit. A phase does not start until the
previous one is marked "done".

| Phase | Title | Status |
|----|-------|-------|
| 1 | Project skeleton, config, database, first admin panel | ✅ done |
| 2 | User identity and the WebSocket gateway | ✅ done |
| 3 | Rooms (create/edit/delete/password/capacity/purge) | ✅ done |
| 4 | Text chat, stickers, typing indicator | ✅ done |
| 5 | Roles, permissions and moderation | ✅ done |
| 6 | File and image transfer (temporary) | ✅ done |
| 7 | Voice/video/screen share with LiveKit | ✅ done |
| 8 | Real-time paint board | ✅ done |
| 9 | Bots (music bot and its controls) | ✅ done |
| 10 | Full admin panel + live server control | ✅ done |
| 11 | Hardening, testing, packaging and release | ✅ done |

---

## Phase 1 — skeleton (done)

- Go module, `cmd/` + `internal/` layout
- CGo-free SQLite (`modernc.org/sqlite`) + a forward-only migration system
- Config in the database instead of `.env`, with a typed registry and validation
- Structured logging (`log/slog`) with a level that can change at runtime
- HTTP: `GET /healthz` and `GET /api/v1/server-info`
- A stable server identity (`server_uuid`) created on first run
- Interactive command-line admin panel (numbered menu, x-ui style)
- Clean shutdown on SIGINT/SIGTERM

## Phase 2 — user identity and the WebSocket gateway (done)

- Versioned protocol with a `{t, id, d}` envelope — fully documented in [PROTOCOL.md](PROTOCOL.md)
- `GET /ws` endpoint with a mandatory `hello` handshake and a 10-second deadline
- Login with `client_uuid` + `username` + server password (constant-time comparison)
- A `users` table recording first/last seen and visit count
- In-memory session management, heartbeat pings, slow clients dropped
- Reconnecting with the same UUID replaces the previous session without presence noise
- `user.joined` / `user.left` / `user.updated` events and the `rename` message
- Username validation that blocks bidirectional text characters (anti-impersonation)
- A "Known users" section in the admin panel
- Tests: 31 tests/subtests across three packages (gateway, httpapi, session)

## Phase 3 — rooms (done)

- A `rooms` table for the durable part (name, password, capacity, position) + migration 0004
- The `rooms` package: the durable definition and the live in-memory state side by side
- The `Ephemeral` hook for temporary room content; phases 4/6/8 register themselves,
  so the purge logic never needs to know what it is erasing
- `room.list/join/leave/create/update/delete` and the matching events
- Room password (constant-time comparison), capacity, room count limit, unique names
- Content purged when the last member leaves, after a `rooms.purge_grace_sec`
  grace period that is cancelled automatically if somebody returns
- Room membership transferred on reconnect, so a dead session is not left in a room
- A "Rooms" section in the admin panel
- `internal/authz` as the single permission checkpoint — phase 5 replaces it
- End-to-end test against a real server (`internal/app`)

Important note: the role required to enter a room was deliberately deferred to
phase 5, because it means nothing without a role system.

## Phase 4 — text chat (done)

- A per-room in-memory message buffer that registers itself as `rooms.Ephemeral`;
  the phase-3 purge therefore erases chat too, unchanged
- `chat.send/history/edit/delete/typing` and the matching events
- Backwards pagination with `before_seq` and a `has_more` flag
- A buffer message cap (`rooms.history_limit`) that drops the oldest
- Stickers as an ID (`sticker_id`); the image itself lives on the client
- A token-bucket rate limiter in `internal/ratelimit`, separate for sends and typing
- A rejected message does not spend rate quota
- Editing is author-only — not even a moderator may change someone else's text
- `rooms.Manager.OnDelete` so deleting a room also frees its chat memory
- Tests: 31 new tests (chat over the socket + buffer unit + ratelimit unit)

Deferred: replying to a message, room system messages, and a server-hosted
sticker pack — the last one depends on phase 6, since it needs file storage
and serving.

## Phase 5 — roles and moderation (done)

- Migration 0005: the `roles`, `user_roles`, `sanctions` and `mod_log` tables and
  the `rooms.required_role_id` column
- A 12-permission bitmask in `internal/authz` with stable text keys on the wire
- The `internal/access` package: all state in memory (permissions are checked on
  every message and must not touch disk), plus the `authz.Policy` implementation
- Two built-in roles created on first run: "User" (the default for everyone)
  and "Admin" (all permissions). Neither can be deleted.
- The priority rule: you may only act on someone strictly below your rank; and
  you cannot create a role at or above your own rank, or grant a permission you
  do not hold yourself
- `admin.kick/ban/unban/mute/unmute/move/sanctions` and full role CRUD
- Bans are checked during the handshake; a mute is not cleared by reconnecting
- Temporary (`duration_sec`) or permanent bans and mutes, with expired ones purged automatically
- Role-locked rooms plus a permission to bypass the room password
- A moderation log (`mod_log`) shown in the panel
- The "Roles and permissions", "Bans and mutes" and "Moderation log" panel
  sections — the server's first admin is created here
- Tests: 19 new tests in `internal/gateway`

## Phase 6 — files and images (done)

- The `internal/files` package: upload tickets, per-room quota, disk storage, download links
- Upload permission comes from the WebSocket and the bytes go over HTTP — the
  server checks access, size and quota before receiving a single byte
- A single-use upload ticket with a 2-minute lifetime; a short-lived download
  link scoped to the room the user is in right now
- Content type detected from the bytes themselves (`http.DetectContentType`), not the extension
- A JPEG thumbnail using area-averaging downscaling (no new dependency)
- Served with `Content-Disposition: attachment` and `nosniff`
- An uploaded file posts itself into the room as a `file` message
- Bytes deleted from disk when the room is purged or deleted; the whole folder is
  cleared at startup
- Tests: 15 new tests + an extended end-to-end test

Deferred: the server-hosted sticker pack (deferred to this phase back in phase 4)
is still not implemented; the file storage layer exists now, but unlike room
files, stickers must be **permanent** and need a separate path.

## Phase 7 — voice/video/screen share (done)

- The `internal/media` package: LiveKit token signing (JWT/HS256) and a Twirp
  client for RoomService — without pulling in the LiveKit SDK, which would bring
  a full WebRTC stack into a server that never touches an RTP packet
- Three new permissions: `speak`, `publish_video`, `share_screen` + migration
  0006, which grants the new bits to existing roles
- The token grant mirrors the user's permissions exactly (`canPublishSources`), so
  a misbehaving client is rejected by LiveKit itself
- The LiveKit room name is the TamizChat room ID, participant identity is `client_uuid`
- Moderation applied: mute → `UpdateParticipant`, kick/ban/leave → `RemoveParticipant`,
  room deletion → `DeleteRoom`
- `media.set_state` for the mic/camera/screen icons, reset when leaving a room
- Every LiveKit call is best-effort and has a timeout; a media outage does not take chat down
- Tests: 24 new tests, including a fake LiveKit that inspects the server's real calls

Deliberate decision: "currently speaking" state is not kept server-side — it
changes several times a second and LiveKit already gives clients the active
speaker event.

Deferred: LiveKit webhooks (so the server knows who actually joined the media
session). Not needed yet, since every room membership change originates here.

## Phase 8 — paint board (done)

- The `internal/paint` package: a per-room board that registers itself as `rooms.Ephemeral`
- Strokes are **streamed**: `paint.begin` / `paint.append` / `paint.end` — the
  drawing is seen as it is drawn, not after the mouse is released
- `paint.append` deliberately has no reply (the most frequent message in the system)
- `paint.undo` removes the user's own last stroke; `paint.clear` takes a `mine`
  or `all` scope (the latter needs `moderate_chat`, since it destroys other people's work)
- `paint.state` for newcomers; the server does not push it automatically
- Normalized 0..1 coordinates + NaN/infinity discarded (a broken client must not
  break everyone else's rendering)
- The `paint` permission + migration 0007, stroke/point caps, a separate rate
  limit, and muted users blocked
- Tests: 23 new tests (over the socket + board unit)

## Phase 9 — bots (done)

- Migration 0008 + a `bots` table; create/edit/delete from the panel (option 10)
- **Audio path: LiveKit Ingress.** The backend touches no audio bytes; Ingress
  fetches the file from `GET /api/v1/bot-stream/{id}?token=` and publishes it itself
- Folder walking (including subfolders), sorted by name, audio detected by extension
- `bot.list` / `bot.control` (play/stop/next/prev/select) / `bot.move`
- `bot.state` broadcast to everyone on every change
- The LiveKit webhook (`POST /api/v1/livekit/webhook`) with signature verification:
  end-of-track is learned here and the queue advances automatically
- A single-use playback token + a re-check that the file really is inside the bot's folder
- Tests: 26 new tests, including a fake Ingress and an independently signed webhook

Deliberate decisions:
- **No pause** — with Ingress, resuming restarts the track from the beginning, and
  a button that jumps out of the middle of a song is worse than no button at all.
- **No server-side volume control** — each listener sets a bot's volume in their
  own client, which is both more correct and needs no audio manipulation.

## Phase 10 — full admin panel (done)

- The `internal/control` package: a control socket on loopback with a random port,
  a fresh token each run, and a `control.json` file next to the database at mode 0600
- **The project's main debt is paid off:** live `reload` for settings, roles and
  bans, rooms and bots. A panel change no longer waits for a restart
- On reload, a deleted room ejects its members with the reason `room_deleted`
- A role granted from the panel applies to users who are online right now
- The "Running server" menu: live status (uptime, online count, goroutines,
  memory), the online user list, kick, server-wide notice, stop bots
- The "systemd service" menu: a unit file for this exact installation, filled in
  and ready to write — with `ProtectSystem` and `ReadWritePaths` hardening
- After each panel edit, if the server is running, it asks right there:
  "apply now?"
- Tests: 9 new tests against a real server, including a wrong token and a stale
  endpoint file

Deliberate decision: `network.listen_addr` is the one setting reload does not
apply; the listener binds once at startup. The panel says so explicitly.

Deferred: start/stop/restart directly from the panel. On Linux that is systemd's
job (the panel writes the unit file), and implementing process management inside
the panel would duplicate what the OS does better.

## Phase 11 — hardening and release (done)

- The `internal/guard` package: a per-IP concurrent connection cap plus a new
  connection rate cap. Rejection happens **before** the upgrade, so a connection
  flood costs one HTTP response rather than a full WebSocket
- Real client IP behind a reverse proxy via the `network.trusted_proxies` list;
  X-Forwarded-For is believed only from listed addresses
- Panic recovery in both the per-frame WebSocket path and the HTTP layer — one
  broken message from one client must not take the whole server down
- Security headers on every response (`nosniff`, `DENY`, a fully closed CSP)
- Optional in-server TLS (`tls.*`) with a TLS 1.2 minimum and a certificate check
  at startup; the recommended setup is still a reverse proxy
- Database backup and restore with `VACUUM INTO` (safe while running), N copies
  retained, and a restore that does not delete the current copy — panel option 13
- A `Makefile` with version/commit injection and four-platform builds (`make release`)
- [docs/DEPLOY.md](DEPLOY.md): the full operator guide covering nginx, LiveKit,
  Ingress, backups, hardening and troubleshooting
- Tests: 27 new tests, including hostile frames (truncated JSON, wrong types,
  null, binary frames) which must all get a reply rather than break something


---

## After phase 11

The backend is complete. Work that was deliberately deferred and can be picked
up whenever it is needed:

- Replying to a message, and room system messages
- A server-hosted sticker pack (needs permanent storage separate from the
  temporary room files)
- Real pause for the music bot (needs the audio path changed from Ingress to
  server-sdk-go)
- More LiveKit webhooks, to know who is actually in the media session
- `go test -race` in CI (the dev machine had no gcc)

The project's next step: **the C# / WinUI 3 frontend**, built against
[PROTOCOL.md](PROTOCOL.md).
