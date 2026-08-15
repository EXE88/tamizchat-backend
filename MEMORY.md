# MEMORY — the TamizChat project memory

This file is the memory carried between chats. Read it first at the start of any
new conversation, and update "Current status" and "Decisions" at the end of any
piece of work.

Last updated: 2026-08-12 — **the backend is complete** (phase 11)

---

## What the project is

TamizChat is a self-hosted communications client; a mix of Discord and Google
Meet with TeamSpeak's philosophy: anyone runs and configures the backend on their
own server, and the user enters that server's address and port in the app and
connects.

- Backend: **Go** — the `backend/` folder (the git repo lives here)
- Frontend: **C# / WinUI 3** — starts once the backend is entirely finished

## The whole project's features

The backend's responsibility: text chat, voice chat, video chat, file and image
transfer, screen sharing, bots, a paint board, TeamSpeak-style moderation, custom
roles, stickers.

The frontend's responsibility (the backend is not involved): built-in sound
effects and customising them, voice changer, a sound played on kick, a sound
played on ban, microphone and listening settings, key bindings for shortcuts.

## Locked architecture decisions

| Topic | Decision |
|-------|----------|
| Backend language | Go (currently go1.26) |
| Media (voice/video/screen share) | **LiveKit as a separate service**; the backend only issues tokens and manages rooms |
| Signalling and events | WebSocket with a versioned JSON protocol |
| Durable storage | **CGo-free SQLite** (`modernc.org/sqlite`) — a single `.db` file |
| Config | Inside SQLite, **not** a `.env` file |
| Admin panel | An interactive command-line panel in the style of **x-ui**: run `tamizchat` on the server and a numbered menu opens |
| Authentication | No real auth — each client generates a **UUID** at install time, keeps it safe and always sends it; the backend knows and manages the user by that UUID |
| Room content persistence | **Temporary**; when the last person leaves, the room's chat, files and images are erased from memory |

## Key logic that must be respected

- The admin creates/edits/deletes rooms: name, password, role-based access,
  capacity.
- Room content lives only in memory. Purging happens after a short grace period
  (`rooms.purge_grace_sec`) so a momentary drop does not lose the chat.
- Bots: several at once, added/removed from the panel. A music bot is configured
  with a name and a music folder, connects to the server like an ordinary user,
  the admin moves it between rooms, and the client shows a small panel next to
  its profile / on right-click for next/prev/stop.

## Current status

**All 11 backend phases are done and tested** (244 tests, all green), plus the
post-roadmap work the client's admin panel needs — see "Work added after phase
11" below.
The backend is complete as far as the roadmap goes; the project's next step is
the WinUI 3 frontend. Phase details are in [docs/ROADMAP.md](docs/ROADMAP.md) and
the wire contract is in [docs/PROTOCOL.md](docs/PROTOCOL.md).

```
backend/
  cmd/tamizchat/main.go          single command: panel (default) | run | version
  internal/app/app.go            server wiring and lifecycle
  internal/config/schema.go      typed settings registry (the panel's source of truth)
  internal/config/config.go      live config view + Set/Reset/Watch
  internal/storage/store.go      opens SQLite with WAL
  internal/storage/migrations.go forward-only migrations (0001 to 0008)
  internal/storage/settings.go   settings + server_uuid + NewUUID
  internal/storage/users.go      the users table: first/last seen, visit count
  internal/storage/rooms.go      CRUD for durable room definitions
  internal/storage/roles.go      roles and their assignment to users
  internal/storage/sanctions.go  bans/mutes + the moderation action log
  internal/protocol/             the wire contract: envelope, message types, error codes
  internal/session/session.go    one session + a non-blocking outbound queue + roomID
  internal/session/manager.go    session registry, broadcast, duplicate-UUID replacement
  internal/session/validate.go   UUID and username validation
  internal/textutil/             shared normalization for usernames and room names
  internal/rooms/room.go         definition + members + the Ephemeral hook for temporary content
  internal/rooms/manager.go      room CRUD, join/leave, membership transfer, purging
  internal/chat/buffer.go        per-room message buffer (an Ephemeral implementation)
  internal/chat/manager.go       send/edit/delete/history/typing + rate limits
  internal/ratelimit/            a general-purpose token bucket
  internal/files/manager.go      upload tickets, quota, download links
  internal/files/files.go        per-room files (an Ephemeral implementation)
  internal/files/thumbnail.go    thumbnails with no external dependency
  internal/media/token.go        LiveKit JWT signing + grants
  internal/media/client.go       Twirp client for LiveKit's RoomService
  internal/media/manager.go      token issuance and applying moderation to media
  internal/paint/board.go        per-room board (an Ephemeral implementation)
  internal/paint/manager.go      begin/append/end, undo, clear, rate limits
  internal/media/ingress.go      LiveKit Ingress + webhook signature verification
  internal/bots/library.go       scanning the music folder
  internal/bots/manager.go       playback queue, controls, Ingress coordination
  internal/authz/                permission bitmask + the Policy interface
  internal/access/               roles, permissions, bans and mutes — all in memory
  internal/gateway/gateway.go    upgrade + handshake
  internal/gateway/pump.go       read/write pumps, dispatch, keepalive
  internal/gateway/rooms.go      room.* handlers and error mapping
  internal/gateway/chat.go       chat.* handlers and error mapping
  internal/gateway/admin.go      kick/ban/mute/move + broadcasting moderation events
  internal/gateway/roles.go      role CRUD and assignment, computing a user's permissions
  internal/gateway/files.go      upload tickets and download links over the socket
  internal/gateway/media.go      media tokens and mic/camera state
  internal/gateway/paint.go      paint.* handlers and error mapping
  internal/gateway/bots.go       bot.* handlers and error mapping
  internal/httpapi/bots.go       the music file for Ingress + the webhook endpoint
  internal/app/webhook.go        routing the LiveKit webhook to the bots
  internal/panel/bots.go         creating and editing bots in the panel
  internal/control/             the control socket between the panel and a running server
  internal/app/control.go       implementation of the control commands
  internal/rooms/reload.go      live synchronisation of room definitions
  internal/panel/live.go        the running-server control menu
  internal/panel/service.go     generating the systemd unit file
  internal/guard/               per-IP connection cap and connection rate cap
  internal/httpapi/clientip.go  the real IP behind a reverse proxy
  internal/storage/backup.go    backups with VACUUM INTO and a safe restore
  internal/panel/backup.go      backup and restore in the panel
  Makefile                      versioned, multi-platform builds
  docs/DEPLOY.md                the full operator guide
  internal/httpapi/files.go      POST /api/v1/upload and GET /api/v1/file/{id}
  internal/panel/access.go       roles, bans and the log in the panel
  internal/httpapi/              /healthz, /api/v1/server-info and /ws
  internal/logging/              slog with a runtime-changeable level
  internal/version/              version and commit (injectable with ldflags)
  internal/app/smoke_test.go     end-to-end test against a real server
  docs/ROADMAP.md                the full phase breakdown
  docs/PROTOCOL.md               the protocol document for the WinUI client
```

### Rules settled in phase 2

- One `client_uuid` = one presence. A new connection with the same UUID closes
  the previous session with the reason `replaced_by_new_connection` and **no**
  join/leave event is published to anyone else (so a momentary drop is invisible
  to others).
- A slow client (outbound queue full) is disconnected rather than allowed to grow
  the server's memory.
- Usernames: control characters and bidirectional text characters are forbidden
  (anti-impersonation); uniqueness is case-insensitive and only among online
  users.
- All writes to the socket happen only from writePump (a WebSocket has a single
  writer).

### Rules settled in phase 3

- A room's definition is permanent, its content temporary. The `rooms.Ephemeral`
  hook is where phase 4 (messages), phase 6 (files) and phase 8 (drawings)
  register themselves; the purge logic never needs to know what it is erasing.
- `room.member_joined/left` are broadcast **server-wide**, not just inside the
  room — because the client shows the whole room tree like TeamSpeak. That is
  also why no separate `user.updated` is sent when someone moves rooms (one event
  per event). `user.updated` is for name changes only.
- Room moderation actions take the actor's ID so they can be excluded from the
  general broadcast and not receive a duplicate frame (their own reply is
  correlated by `id`).
- Every permission change must go through `internal/authz`; right now it is
  `OpenPolicy` and allows everything. **Do not run the server without a server
  password until phase 5.**
- HTTP middleware must implement `Unwrap`/`Hijack` or the WebSocket upgrade
  breaks with a 501 (this bug really happened and has a regression test).

### Rules settled in phase 4

- Each room's chat buffer registers itself as a `rooms.Ephemeral`. No new purge
  code was written; the phase-3 path just works. Phases 6 and 8 must do exactly
  the same.
- **Editing is author-only, even for a moderator.** A moderator can delete
  someone else's message but not edit it — otherwise you could put words in
  someone's mouth. This split is settled in `authz.Policy.CanModerateChat` and in
  the tests.
- A message the server rejects does not spend rate quota; otherwise someone
  writing a long message gets silenced for spamming.
- A deleted message is deleted completely (no tombstone) — which fits the
  temporary nature of a room.
- `seq` only means anything inside one live room and continues across a purge (it
  is not reset) so older clients are not confused.
- `rooms.Manager.OnDelete` is the release point for per-room memory; phase 6 must
  use it too.
- **Test trap:** every test client must consume all broadcast frames, or the next
  `expect` sees the previous frame. For frames whose order is not part of the
  contract, use `expectFrames`. The test fixture deliberately raises the rate
  limits; the rate tests lower them themselves.

### Known current limitations

- Moderation actions (kick/ban/mute/move) only work on an **online** user;
  banning an offline user is done from the panel. Unbanning works offline too.
- Role changes are applied to online sessions with `refreshRoles`.
- Chat: replying to a message and room system messages do not exist yet.
- The server-hosted sticker pack does not exist yet: the file infrastructure is
  there now, but unlike room files, stickers must be **permanent** and need a
  separate path.
- `go test -race` does not work on this Windows machine because gcc is not
  installed; the tests were run without the race detector.
- ~~Panel changes need a restart~~ — solved in phase 10.
- The panel cannot start/stop/restart the server directly: on Linux that is
  systemd's job and the panel generates its unit file.

### Rules settled in phase 5

- **The priority rule:** you may only act on someone whose priority is strictly
  lower. Two admins at the same priority cannot kick/ban/mute each other. You
  also cannot create or grant a role at or above your own priority, and cannot
  give a role a permission you do not hold. Together these close off arbitrary
  self-promotion.
- Permissions are **text keys** on the wire, not numbers, so an older client can
  understand the ones it knows. The bits must never be reordered (they are stored
  in the database).
- All access state lives in memory because it is checked on every message.
- `access.Manager` is simultaneously an `authz.Policy`, a `rooms.Access` and a
  `chat.Sanctions` — one source of truth.
- **A server-initiated close uses `CloseNow`, not a graceful close.** A graceful
  close waits for the client's reply, and someone who has just been kicked stops
  reading; the result was a session that stayed alive for seconds after the kick
  (this bug really happened and has a test). The reason is sent beforehand as an
  application frame.
- `admin.move` deliberately ignores the room's password, role and capacity.
- The server's first admin can only be created from the panel; the panel is not
  subject to the priority rule, because whoever has access to it owns the server.

### Rules settled in phase 6

- **Permission from the socket, bytes over HTTP.** The client first sends
  `file.upload_request` and gets a ticket; before receiving a single byte the
  server checks permission, size and quota. The ticket is single-use. This
  pattern should be repeated for any future bulk transfer.
- The file type is detected from **the bytes themselves**, not the extension or
  the client's claim.
- Downloads are always `attachment` + `nosniff` so an uploaded file cannot
  execute on the server's domain. The one exception is the thumbnail, which is
  always JPEG.
- A download link is scoped to the room the user is in **right now**; an ID
  leaked from another room does not work.
- Files are stored on disk under a random ID; the user's name is for display only.
- Like chat, files are `rooms.Ephemeral` and are removed from **disk** when the
  room is purged. The whole upload folder is cleared at startup.
- Image downscaling was written with area averaging rather than nearest-neighbour,
  because nearest looks grainy and broken on a real photo. No new dependency.

### Rules settled in phase 7

- **The LiveKit SDK was not added.** The token is a plain JWT/HS256 and
  RoomService is Twirp over HTTP+JSON; both were implemented directly. The SDK
  would bring a full WebRTC stack into a server that never touches an RTP packet.
- The token grant must mirror the permissions (`canPublishSources`). The `can_*`
  flags are for the user interface only; real enforcement is on LiveKit's side.
- **The permission bits are locked** by the `TestPermissionBitsAreStable` test.
  Every new permission = a new bit at the end + a migration granting it to
  existing roles (migration 0006 is the pattern). Reordering bits means existing
  roles change meaning.
- Every LiveKit call is **best-effort with a timeout** and runs in its own
  goroutine. LiveKit being broken or absent must not take chat down.
- A mute has to reach LiveKit (`UpdateParticipant`), or a muted user is silent in
  chat but still talking on voice.
- "Currently speaking" is deliberately not kept server-side — LiveKit gives the
  client the active speaker event itself. Only mic/cam/screen, which the client
  reports itself, is kept, and it resets on leaving a room.
- `rooms.Manager.OnMemberLeft` is the central room-exit hook (a normal leave, a
  room switch, an admin move, a disconnect). Later phases should use the same.

### Rules settled in phase 8

- **Strokes are streamed** (begin/append/end). A drawing must be seen as it is
  drawn; sending the finished stroke after the mouse is released ruins the feel
  of the product.
- `paint.append` deliberately **has no reply** — it is the most frequent message
  in the whole system and the client has already drawn the point. Only errors get
  a frame.
- Coordinates are **normalized 0..1**, not pixels, or the drawing lands in the
  wrong place at another resolution. The server discards NaN and infinity — one
  broken client must not wreck everyone else's rendering.
- A full board **rejects** a new stroke rather than dropping the oldest. Unlike
  chat (which has a ring buffer), silently erasing the start of a drawing is
  worse than returning an error.
- `clear` with scope `all` requires `moderate_chat` because it destroys other
  people's work; `mine` is open to everyone.
- A muted user cannot draw (same logic: scribbling = shouting).
- The "new permission = new bit + migration" pattern was applied again (migration
  0007). This is now settled procedure.

### Rules settled in phase 9

- **The bot's audio path is LiveKit Ingress** (the user's choice). The backend
  touches no audio byte; Ingress fetches the file from an HTTP endpoint on this
  same server. That means the LiveKit SDK and pion are still not in the project.
- **A new requirement for the operator:** `network.public_host` must have a value,
  or Ingress does not know where to fetch the file from. The server cannot guess
  it and returns an explicit error with guidance.
- **There is deliberately no pause** — with Ingress, resuming means starting the
  track over. A button that jumps out of the middle of a song is worse than not
  having it. If the audio path ever changes (server-sdk-go), a real pause becomes
  possible.
- **Volume control is not server-side** — each listener adjusts it in their own
  client.
- The end of a track is learned only from the **LiveKit webhook**. The signature
  (a JWT whose `sha256` claim equals the body hash) is verified before anything
  else happens; this endpoint has no other authentication.
- A bot's LiveKit identity is `bot-<id>` so it can never be confused with a
  `client_uuid`.
- **Test trap:** the queue is sorted by **file name**. A test that names files
  `one/two/three` walks into alphabetical ordering; use numbered names.
- The "actor excluded from the broadcast, their own reply correlated by `id`"
  pattern was applied again (this time for bots). It is now a fixed project rule.

### Rules settled in phase 10

- **The "needs a restart" debt is paid off.** The panel still writes to the
  database first, then tells the running server to `reload`. Any new component
  that holds state in memory must also have a `Reload` and be wired into
  `controlHandler.Reload`.
- The control channel: TCP on **loopback with a random port** + a fresh token
  each run, in `control.json` next to the database at mode 0600. The trust
  boundary is the database's boundary: anyone who can read the data folder
  already owns everything.
- The token is compared with `subtle.ConstantTimeCompare`, even on loopback.
- The endpoint file is removed cleanly on shutdown, but a hard kill leaves it
  behind, so the client **must dial** rather than trust the file's existence (it
  has a test).
- `network.listen_addr` is the only setting reload does not apply — the listener
  binds once. The panel says so explicitly.
- Reloading rooms does not touch membership, except for a room that has been
  deleted; there the members are ejected with the reason `room_deleted`.
- In the panel package, the name `ok` was banned for a helper function (it
  collided with `v, ok :=`); it is now `green`.

### Rules settled in phase 11

- **Reject before the upgrade.** The connection cap is checked in `ServeHTTP`
  before `websocket.Accept`, so a connection flood costs one HTTP response rather
  than a full WebSocket.
- Behind a reverse proxy, `network.trusted_proxies` **must** be set, or every
  connection appears to come from `127.0.0.1` and the per-IP cap is meaningless.
  And without it, believing X-Forwarded-For means anyone can forge an address.
  The XFF chain is read **right to left** and trusted hops are skipped.
- A panic in one frame's handler loses only that frame. In the recover, always
  write the type assertion checked (`err, ok := rec.(error)`) or a panic with a
  string panics again inside the recover itself.
- Backups are taken with `VACUUM INTO`, not a file copy — a plain copy of a
  database being written to can catch a half-written page or leave the WAL behind.
- A restore **sets the current database aside** rather than deleting it; and old
  `-wal`/`-shm` files must be removed or the restored database is corrupted.
- `WriteTimeout` is **not** set on the http.Server and must not be: sockets stay
  open for a long time and that timeout kills the WebSocket.

### Work added after phase 11, for the client's admin panel

The roadmap was finished, but the WinUI client's in-client admin panel needs
things the wire did not have. Added since:

- **`tag_style` on a role** (migration 0009): opaque JSON the server stores and
  echoes without ever looking inside, so new visual options for a role's tag cost
  no protocol change. `color` is kept in step with it by the client.
- **Bot lifecycle over the wire** — `bot.create` / `bot.update` / `bot.delete`,
  plus the `bot.removed` event. Creating a bot no longer means SSH-ing in.
  - A new permission, **`manage_bots`** (bit 65536, migration 0010), separate
    from `control_bots`: driving the music is an everyday job, configuring the
    server's bots is an administrative one. Only `role-admin` is granted it.
  - **No folder field on the wire, deliberately.** That path is opened by the
    server, so letting a client name one would turn bot creation into an
    arbitrary directory read through the stream endpoint. A client-created bot
    gets `<bots.dir>/<bot id>` and its music arrives by upload; a panel-created
    bot keeps the operator's own path.
  - Delete removes the folder **only when it is the one we made** (compared
    against `managedFolder`). An operator's own folder is never touched.
  - Disabling a bot stops it. A switch that says "off" while the room still hears
    music is a bug, so `Update` routes through `stop` when `enabled` goes false.
  - `bots.dir` is a new setting in a new `bots` config section.
- **Bot playlists** (migration 0011: `bot_playlists` + `bots.playlist_id`) —
  named playlists per bot, filled by upload from the client.
  - **Layout:** `<bots.dir>/<bot id>/default` is the bot's own library and
    `<bots.dir>/<bot id>/<playlist id>` is a playlist. The library is a folder
    *beside* the playlists, never above them — `scanFolder` walks subfolders, so
    a nested playlist would be played twice. This was a real bug, caught by a
    test, before the layout changed.
  - Playlists always live under the server's own storage, even for a bot whose
    `folder` an operator typed in. We do not create folders inside somebody
    else's music directory.
  - Upload reuses the phase-6 pattern — permission over the socket, bytes over
    HTTP (`POST /api/v1/bot-track?token=`), single-use ticket. Unlike a room
    file, a track **keeps its file name**, because the queue is the folder
    listing and the title is that name; so the name is sanitized, must end in an
    accepted audio extension, and collides into "name (2).mp3".
  - This music is permanent, so it has its own quota: `bots.quota_mb` per bot
    and `bots.max_track_mb` per track.
  - Selecting or deleting a playlist stops playback: the queue becomes a
    different list, and an index carried across it lands on an unrelated track.
  - The upload ticket carries the uploader's UUID purely so the actor can be
    left out of the `bot.state` broadcast — the same rule as everywhere else.
  - `bot.queue` finally exists on the wire; `Manager.Queue` had been unreachable
    since phase 9.
  - Playlists are created and filled from the client only. The CLI panel still
    owns folder-based bots, but its bot list now shows **what a bot actually
    plays from** ("playlist: Evening set") and counts that folder — reporting
    the `folder` column would have told an operator that a bot with three tracks
    had none.
  - `bot.queue` takes an optional `playlist_id`, so a playlist can be inspected
    and tidied before it is switched to. Without it the client could only ever
    see the playlist that was already playing.
  - **The bug that only a real run could find:** `withinRoot` compared an
    absolutized root against a *relative* path, and `filepath.Rel` refuses that
    pair — so every upload was rejected with `track_not_audio` on a stock
    configuration, while every test passed because tests use a temp directory,
    which is absolute. Both sides are absolutized now, and
    `internal/bots/paths_test.go` pins it. The lesson is worth keeping: a
    default-valued relative path is a case the tests were structurally unable to
    reach.

### The bot audio path had never actually played a note

Everything below was found by running it against a real LiveKit and Ingress for
the first time. Phase 9's tests used a fake LiveKit, which happily accepted
calls a real one refuses.

- **The server's own LiveKit token lacked `ingressAdmin`.** `roomAdmin` does not
  cover the Ingress service, so every CreateIngress answered
  `401 permissions denied`. Pinned now by `internal/media/grants_test.go`, which
  decodes the token rather than trusting a fake to check it.
- **LiveKit's Twirp API answers in snake_case** (`ingress_id`) while its
  webhooks are camelCase (`ingressId`). Reading only the camelCase spelling made
  a successfully created ingress look like a failure — and left it running.
  `IngressInfo` now reads both.
- **`ResolveTrack` validated the track against `def.Folder`**, the bot's own
  library, so every track in a playlist was refused: Ingress fetched a 404 and
  the bot "played" silence. It now checks against whatever the bot plays from.
- **A bot whose tracks fail instantly used to loop forever.** End-of-track
  advances the queue, so an undecodable file or an unreachable Ingress produced
  a new ingress every 1.5 seconds, indefinitely. Three consecutive tracks that
  end within 3 seconds now stop the bot with an explicit warning.
- **A relative `bots.dir` is resolved next to the database**, not against the
  working directory. It had been landing in `/data/data/bots` in the container,
  and one `WorkingDirectory=` away it would have been outside the data volume —
  for music that is permanent, unlike room files.
- **A fetch URL belongs to a track, not to a bot.** The token used to live on
  the bot, so starting the next track revoked the previous one's URL: an Ingress
  still pulling that track got a 404, reported the track as ended, and the queue
  advanced — which started the next track and went round again, several times a
  second. Grants are now per track, carry the file they were minted for (so a
  fetch can never be answered with whatever the queue moved on to), expire on
  their own, and are dropped when the bot is deleted. Pinned by
  `internal/gateway/botstream_test.go`.
- **`livekit.api_url`** is a new, optional setting: the address *this server*
  reaches LiveKit at, when it differs from the one clients use. One setting
  could not serve both audiences — a container reaches LiveKit by a name that
  means nothing on a user's machine. Empty keeps the old behaviour.
- **Deleting a bot is idempotent.** A row deleted from the panel without a
  reload left the bot in memory, and delete refused on the missing row, so it
  could never be removed from a client. That is what "a hardcoded bot that will
  not delete" actually was.

## Final backend status

Work that was deliberately deferred is listed at the end of `docs/ROADMAP.md`
(reply, server-side stickers, real bot pause, more webhooks, race in CI).
The backend now has everything the client's admin panel asked for; the
remaining work on the bots feature is the **Bots tab in the WinUI client**.

## Next step

**The frontend: C# and WinUI 3.** The full wire contract is in
`docs/PROTOCOL.md` and everything the client needs is there: the message
envelope, the handshake, every message type, the stable error codes, and the
file/media/paint/bot flows.

## Working rules

- Everything backend-related happens inside `backend/` only.
- Published migrations are never edited; a new migration is added instead.
- Every new setting must be registered in `internal/config/schema.go` so it shows
  up in the panel automatically.
- After each phase: update the ROADMAP table and the "Current status" section of
  this file.
- **The whole project is written in English** — panel UI, error messages, code
  comments and documentation. Persian does not render well in terminals.
