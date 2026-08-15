# Client ↔ server protocol

Current version: **1** (`protocol_version` in `/api/v1/server-info`)

The connection is a WebSocket to `ws://<host>:<port>/ws`.

## Envelope format

Every frame is a JSON object with this shape:

```json
{ "t": "<message type>", "id": "<optional request id>", "d": { } }
```

- `t` — the message type
- `id` — if the client sends one, the server's reply carries the same `id` so the
  client can correlate request and response. Messages the server originates
  (someone joining or leaving, for example) have no `id`.
- `d` — the payload

The maximum size of an inbound frame is **64 KB**.

## Handshake

The first frame after the socket opens **must** be `hello`, and it must arrive
within 10 seconds of connecting, or the server closes the connection.

```json
{ "t": "hello", "id": "h1", "d": {
    "client_uuid": "11111111-1111-4111-8111-111111111111",
    "username": "Daniel",
    "password": "",
    "protocol": 1,
    "client_version": "0.1.0"
}}
```

`client_uuid` is the fixed UUID the client generates at install time and keeps
somewhere safe. It is the user's identity on every server.

A successful reply:

```json
{ "t": "welcome", "id": "h1", "d": {
    "session_id": "…",
    "protocol": 1,
    "server_uuid": "…",
    "server_name": "TamizChat Server",
    "welcome_message": "Welcome to the server!",
    "heartbeat_sec": 30,
    "you":   { "client_uuid": "…", "username": "Daniel", "joined_at": 1760000000,
                "room_id": "", "roles": ["role-admin"], "muted": false },
    "users": [ User ],
    "rooms": [ Room ],
    "roles": [ Role ],
    "permissions": ["send_messages", "manage_rooms", "…"],
    "limits": { "username_min": 3, "username_max": 24, "max_users": 200,
                "message_max": 2000, "history_limit": 500, "stickers_enabled": true }
}}
```

`welcome.rooms` carries the whole room tree along with each room's members, so
the client can build its interface immediately without an extra request.

## Client → server messages

| Type | Payload | Notes |
|-----|-------|-------|
| `hello` | above | Once only, at the start of the connection |
| `ping` | — | Replied with `pong` carrying the same `id` |
| `rename` | `{"username": "new name"}` | Change the display name |
| `room.list` | — | Replied with `room.list` and the whole room tree |
| `room.join` | `{"room_id", "password"}` | Join a room; the previous one is left automatically |
| `room.leave` | — | Leave the current room |
| `room.create` | `{"name", "password", "capacity", "required_role_id"}` | Requires the `manage_rooms` permission |
| `room.update` | `{"room_id", "name"?, "password"?, "capacity"?, "position"?, "required_role_id"?}` | Only the fields present are changed |
| `room.delete` | `{"room_id"}` | Requires the `manage_rooms` permission |
| `chat.send` | `{"text"}` or `{"sticker_id"}` | In the current room; exactly one of the two fields |
| `chat.history` | `{"before_seq"?, "limit"?}` | Backwards pagination |
| `chat.edit` | `{"message_id", "text"}` | Author only |
| `chat.delete` | `{"message_id"}` | The author, or a moderator for someone else's message |
| `chat.typing` | `{"typing": true}` | No reply; not stored either |
| `admin.kick` | `{"client_uuid", "reason"}` | Requires the `kick` permission |
| `admin.ban` | `{"client_uuid", "reason", "duration_sec"}` | `0` means permanent |
| `admin.unban` | `{"client_uuid"}` | Works on offline users too |
| `admin.mute` | `{"client_uuid", "reason", "duration_sec"}` | Does not close the connection |
| `admin.unmute` | `{"client_uuid"}` | |
| `admin.move` | `{"client_uuid", "room_id"}` | An empty `room_id` means "out of the room" |
| `admin.sanctions` | — | The list of active bans and mutes |
| `admin.role.list` | — | Open to everyone (to show names and colours) |
| `admin.role.create` | RoleSpec | Requires the `manage_roles` permission |
| `admin.role.update` | RoleSpec with a `role_id` | |
| `admin.role.delete` | `{"role_id"}` | Built-in roles cannot be deleted |
| `admin.role.grant` | `{"client_uuid", "role_id"}` | |
| `admin.role.revoke` | `{"client_uuid", "role_id"}` | |
| `file.upload_request` | `{"name", "size"}` | Upload permission, **before** any bytes are sent |
| `file.download_token` | `{"file_id"}` | A short-lived download link |
| `media.token` | — | LiveKit credentials for the current room |
| `media.set_state` | `{"mic", "cam", "screen"}` | Announce what is switched on |
| `paint.begin` | `{"tool", "color", "width", "points"}` | Start a stroke; the reply carries `stroke_id` |
| `paint.append` | `{"stroke_id", "points"}` | Add points to a stroke being drawn |
| `paint.end` | `{"stroke_id"}` | End of the stroke |
| `paint.undo` | — | Remove your own last stroke |
| `paint.clear` | `{"scope": "mine"\|"all"}` | Clear; `all` requires the `moderate_chat` permission |
| `paint.state` | — | The whole board, for newcomers |
| `bot.list` | — | The bots and their current state (open to everyone) |
| `bot.control` | `{"bot_id", "action", "track_index"?}` | Requires the `control_bots` permission |
| `bot.move` | `{"bot_id", "room_id"}` | An empty `room_id` means "out of the room" |
| `bot.create` | BotSpec | Requires the `manage_bots` permission |
| `bot.update` | BotSpec with `bot_id` | Requires the `manage_bots` permission |
| `bot.delete` | `{"bot_id"}` | Requires the `manage_bots` permission |
| `bot.queue` | `{"bot_id"}` | The bot's full track list (open to everyone) |
| `bot.playlist.list` | `{"bot_id"}` | Requires the `manage_bots` permission |
| `bot.playlist.create` | `{"bot_id", "name"}` | Requires the `manage_bots` permission |
| `bot.playlist.rename` | `{"bot_id", "playlist_id", "name"}` | Requires the `manage_bots` permission |
| `bot.playlist.delete` | `{"bot_id", "playlist_id"}` | Deletes its music too |
| `bot.playlist.select` | `{"bot_id", "playlist_id"}` | Empty id = the bot's own library |
| `bot.track.upload_request` | `{"bot_id", "playlist_id", "name", "size"?}` | Returns an upload ticket |
| `bot.track.delete` | `{"bot_id", "playlist_id", "index"}` | Removes one track from a playlist |

`capacity: 0` on creation means "use the server default".

## Server → client messages

| Type | Payload |
|-----|-------|
| `welcome` | above |
| `pong` | — |
| `error` | `{"code": "...", "message": "..."}` |
| `user.joined` | `{"client_uuid", "username", "joined_at", "room_id"}` |
| `user.left` | `{"client_uuid", "username", "reason"}` |
| `user.updated` | A User — for a name change only |
| `room.list` | `{"rooms": [Room]}` |
| `room.joined` | `{"room": Room}` — only to the requester |
| `room.left` | `{"room_id", "reason"}` — only to that user |
| `room.created` / `room.updated` | A Room |
| `room.deleted` | `{"room_id"}` |
| `room.member_joined` / `room.member_left` | `{"room_id", "user", "reason"}` |
| `room.purged` | `{"room_id"}` — the room's temporary content was erased |
| `chat.message` | A Message — both the reply to `chat.send` and the broadcast to the other members |
| `chat.history` | `{"room_id", "messages": [Message], "has_more"}` |
| `chat.updated` | The edited Message |
| `chat.deleted` | `{"room_id", "message_id", "deleted_by"?}` |
| `chat.typing` | `{"room_id", "client_uuid", "username", "typing"}` |
| `user.kicked` / `user.banned` | `{"client_uuid", "username", "by_uuid", "by_username", "reason", "expires_at"}` |
| `user.muted` / `user.unmuted` | Same shape as above |
| `user.roles_changed` | `{"client_uuid", "roles", "permissions"}` — only to that user |
| `admin.sanctions` | `{"sanctions": [Sanction]}` |
| `admin.role.list` | An array of Role |
| `admin.role` | A Role, after creation or an edit |
| `admin.role.deleted` | `{"role_id"}` |
| `admin.ok` | Acknowledgement of a moderation action |
| `file.upload_ticket` | `{"upload_id", "url", "token", "expires_at", "max_size"}` |
| `file.download` | `{"file_id", "url", "thumb_url", "expires_at"}` |
| `media.token` | `{"url", "token", "room", "identity", "expires_at", "can_speak", "can_publish_video", "can_share_screen"}` |
| `media.state` | `{"client_uuid", "room_id", "state": {"mic", "cam", "screen"}}` |
| `paint.begin` | A Stroke |
| `paint.append` | `{"stroke_id", "points"}` |
| `paint.end` | `{"stroke_id"}` |
| `paint.undo` | `{"stroke_id", "room_id"}` |
| `paint.clear` | `{"room_id", "scope", "by"}` |
| `paint.state` | `{"room_id", "strokes": [Stroke], "max_strokes"}` |
| `bot.list` | `{"bots": [Bot]}` |
| `bot.state` | A Bot — every time the bot's state changes, including a new one |
| `bot.removed` | `{"bot_id"}` — that bot no longer exists |
| `bot.queue` | `{"bot_id", "tracks": [BotTrack]}` |
| `bot.playlist.list` | `{"bot_id", "playlists": [BotPlaylist], "active_playlist_id"}` |
| `bot.playlist` | One BotPlaylist, after create or rename |
| `bot.playlist.deleted` | `{"bot_id", "playlist_id"}` |
| `bot.track.upload_ticket` | `{"url", "token", "expires_at", "max_size"}` |
| `server.notice` | `{"text", "from"}` — a notice from the server operator |

The `Room` shape:

```json
{ "id": "…", "name": "…", "has_password": false, "capacity": 25,
  "position": 0, "member_count": 2, "members": [ User ],
  "required_role_id": "" }
```

The room password is never sent to a client; only `has_password`.

The `Message` shape:

```json
{ "id": "…", "seq": 12, "room_id": "…",
  "author": User, "kind": "text",
  "text": "hello", "sticker_id": "",
  "created_at": 1786400000, "edited_at": 0 }
```

`kind` is either `text` or `sticker`. `seq` is an ordering counter within that
one room and only means anything while the room is alive — use it for pagination
via `before_seq`. To fetch older messages, send the `seq` of the oldest message
you hold in `before_seq`; `has_more` tells you whether anything is left.

Important notes about chat:

- Sticker artwork lives on the client; the server only passes a `sticker_id`
  around (allowed pattern: `[A-Za-z0-9._:-]`, up to 64 characters). A
  server-hosted sticker pack is deferred to a later phase.
- Editing is author-only, even for a moderator. A moderator can **delete**
  someone else's message but cannot put words in their mouth.
- A deleted message leaves memory entirely (no tombstone).
- Sending is rate limited (`chat.rate_per_minute` and `chat.rate_burst`). A
  message the server rejects does not spend rate quota.
- `chat.typing` is neither stored nor replied to; if it goes missing, the
  client's own timer clears the indicator.

## Why room membership events are server-wide

`room.member_joined` and `room.member_left` are sent to **every** user on the
server, not just that room's members — because the client, like TeamSpeak, shows
the whole room tree and who is inside each one. That is also why no separate
`user.updated` is sent when someone moves between rooms: one event per move, and
it carries the room ID itself. `user.updated` is for name changes only.

## Room content lifecycle

A room's definition (name, password, capacity) is permanent and lives in the
database. But everything that happens inside a room — messages, files, drawings —
is temporary: when the last person leaves, a short grace period
(`rooms.purge_grace_sec`, 30 seconds by default) is counted, and if nobody comes
back, everything is erased and `room.purged` is broadcast. The grace period
exists so a momentary drop, or stepping out and straight back in, does not
destroy an active conversation.

## Error codes

The codes are stable identifiers; a client should show its own text based on
`code` and not rely on `message` (which is for debugging).

| Code | Meaning |
|----|------|
| `bad_request` | The message format or payload is invalid |
| `handshake_required` | The first message was not hello |
| `handshake_timeout` | hello did not arrive in time |
| `unsupported_protocol` | Incompatible protocol version |
| `invalid_client_uuid` | The client ID is not valid |
| `invalid_username` | The username is not acceptable |
| `username_taken` | That username is currently in use on the server |
| `bad_password` | Wrong server password |
| `server_full` | The server is full |
| `internal_error` | Internal server error |
| `room_not_found` | No such room |
| `room_name_taken` | Duplicate room name |
| `room_bad_password` | Wrong room password |
| `room_full` | The room is full |
| `room_limit_reached` | You have reached the server's room limit |
| `room_invalid_name` | The room name or capacity is not acceptable (`message` is displayable) |
| `not_in_a_room` | The operation requires being in a room |
| `forbidden` | You lack the required permission |
| `banned` | You are banned from the server (`message` holds the reason) |
| `muted` | You are muted and cannot send messages |
| `outranked` | The target ranks equal to or above you |
| `user_not_found` | The user is not online, or no such sanction is recorded |
| `role_not_found` / `role_name_taken` / `role_protected` | Role errors |
| `room_role_required` | You lack the role required to join the room |
| `invalid_input` | The moderation action's input is not valid |
| `uploads_disabled` | File uploads are off on this server |
| `file_too_large` | The file exceeds the size limit |
| `room_quota_exceeded` | The room's file quota is full |
| `file_not_found` | The file no longer exists (the room was purged, or it belongs to another room) |
| `file_invalid` | The file name or content is not valid |
| `media_disabled` | Voice and video are not enabled on this server |
| `paint_disabled` | The paint board is not enabled on this server |
| `paint_board_full` | The board is full; it must be cleared |
| `paint_stroke_not_found` | No such stroke on the board, or it is not yours |
| `paint_invalid` | Invalid tool or colour |
| `bot_not_found` | No such bot |
| `bot_disabled` | That bot is disabled |
| `bot_queue_empty` | The bot's music folder is empty |
| `bot_bad_action` | The bot command is not recognised |
| `bot_name_taken` | Another bot already uses that name |
| `bot_limit_reached` | The server already has as many bots as it allows |
| `playlist_not_found` | No such playlist for that bot |
| `playlist_name_taken` | That bot already has a playlist by that name |
| `playlist_limit_reached` | That bot has as many playlists as it allows |
| `track_not_audio` | The file is not one of the accepted audio types |
| `track_not_found` | No track at that position in the playlist |
| `message_invalid` | The text or sticker is not acceptable (`message` is displayable) |
| `message_not_found` | The message is no longer in the room's memory |
| `stickers_disabled` | Stickers are off on this server |
| `rate_limited` | You sent messages faster than allowed |

## Disconnect reasons (`reason`)

`client_left` · `replaced_by_new_connection` · `timeout` · `server_shutdown` · `slow_consumer`

## Room-leave reasons (`reason`)

`left` · `switched_room` · `disconnected` · `room_deleted`

## Username rules

- Length between `username_min` and `username_max` (3 to 24 characters by default)
- Leading/trailing whitespace is stripped and internal runs collapse to one space
- Control characters and bidirectional text characters (LRM/RLM/LRO/RLO/…) are
  not allowed — they can be used to impersonate another user's name
- Uniqueness is checked **case-insensitively** and only among online users

## Reconnecting

If a client connects with a `client_uuid` that is already in use, the server
closes the previous connection with the reason `replaced_by_new_connection` and
the new session takes its place. No leave or join event is published to anyone
else, so a momentary drop is invisible to other users.

## Heartbeat

The server sends a WebSocket-level ping every `heartbeat_sec` seconds. Standard
libraries answer the pong themselves. A client that does not answer is
disconnected with the reason `timeout`. The application-level `ping` message is
also available for measuring latency.


## Roles and permissions

Each role has a permission bitmask, a **priority** and a colour. A user's
effective permissions are the union of all their roles, plus any role marked
`is_default`, which everyone holds implicitly.

The `Role` shape:

```json
{ "id": "role-admin", "name": "Admin",
  "permissions": ["kick", "ban", "..."],
  "priority": 100, "color": "#e74c3c", "is_default": false }
```

Permissions are stable text keys rather than numbers, so an older client can
still understand the ones it knows:

`send_messages` · `upload_files` · `moderate_chat` · `manage_rooms` ·
`join_locked_rooms` · `bypass_room_password` · `kick` · `ban` · `mute` ·
`move_users` · `manage_roles` · `control_bots` · `manage_bots` · `speak` ·
`publish_video` · `share_screen` · `paint`

`welcome` also carries the server's full role list (`roles`) and your own
permissions (`permissions`) so the client can hide buttons that are of no use to
you. The server re-checks on every request; hiding a button is not security, only
interface manners.

### The priority rule

A moderator may only act on someone whose priority is **strictly lower**. If two
admins share a priority, neither can kick, ban or mute the other. In the same
spirit:

- You cannot create or grant a role at or above your own priority
- You cannot grant a role a permission you do not hold yourself

Those two rules close off arbitrary self-promotion.

### Joining a room

If a room has a `required_role_id`, only holders of that role (or of
`join_locked_rooms`) can enter. A holder of `bypass_room_password` gets in
without knowing the room password.

`admin.move` respects none of this — not the password, not the role, not the
capacity. A moderator move is an explicit decision that already passed a
permission check.

### Bans and mutes

- A ban blocks the **connection**; it is checked during the handshake and returns
  the `banned` code.
- A mute only stops messages from being sent and does not close the connection.
  Reconnecting does not clear it.
- Both can be temporary (`duration_sec`) or permanent (`0`).
- The server's first admin is created from the command-line panel, not from
  inside the app.

### Server-initiated close

When the server closes a connection (kick, ban, shutdown), the reason has
**already** been sent as an ordinary frame (`user.kicked`, `user.banned` or
`error`). The socket close itself does not wait for the client to answer, so you
may not see a standard close frame; rely on the last frame you received.


## Files and images

Files travel over HTTP, not the WebSocket — the socket has a 64 KB per-frame
limit and is meant for control, not megabytes of data. But the **permission**
comes from the WebSocket, so the server can check access, quota and size before
receiving a single byte.

### The upload path

1. The client sends `file.upload_request` with the file's name and size.
2. The server checks: are uploads enabled? does the user hold `upload_files`? are
   they in a room? is the size under the limit? does the room's quota have room?
   If everything passes, it returns a `file.upload_ticket`.
3. The client `POST`s the raw bytes to the ticket's `url` (a raw body, not
   multipart). The token is accepted both in the query string and as
   `Authorization: Bearer`.
4. The server stores the file and posts a `file` message into the room **itself**.
   So everyone — the sender included — receives a `chat.message`.

Each ticket is **single-use** and valid for 2 minutes.

### The download path

Send `file.download_token` with the file ID to get a short-lived link (5 minutes
by default, `uploads.token_ttl_sec`). The link only works for the room you are in
**right now**; a file ID leaked from another room is unusable.

For images a `thumb_url` comes along too — the same link with `&thumb=1`.

The `Attachment` shape:

```json
{ "id": "…", "name": "report.pdf", "size": 12345,
  "mime": "application/pdf", "kind": "file",
  "width": 0, "height": 0, "has_thumb": false }
```

`kind` is either `image` or `file`.

### Security notes the client should know

- The server derives `mime` from the **bytes themselves**, not from the extension
  or the client's claim. A file called `photo.png` that is really plain text gets
  `kind: "file"`.
- Downloads are always served with `Content-Disposition: attachment` and
  `X-Content-Type-Options: nosniff`, so an uploaded file cannot execute on the
  server's domain. Only the thumbnail is `inline`, and it is always JPEG.
- The file name is for display only; on disk the file is stored under a random ID
  and path components (`../`) are stripped from the name.

### Lifecycle

Files are as temporary as chat:

- When a room empties (after the purge grace period) the bytes are **deleted from
  disk**, not merely forgotten.
- The same happens when a room is deleted.
- At server startup the whole upload folder is cleared; no file survives a
  restart, because every file belongs to a live room.


## Voice, video and screen sharing

Media travels through **LiveKit**, which is a separate service. The TamizChat
backend touches no audio or video packet; it only does two things:

1. **Signs a token** the client uses to enter the LiveKit room.
2. **Applies moderation** — someone kicked or muted should lose their microphone
   too, not just their chat.

### Getting credentials

Send `media.token` (with no payload). The reply is for the room you are in
**right now**:

```json
{ "url": "ws://127.0.0.1:7880", "token": "<JWT>",
  "room": "<room_id>", "identity": "<client_uuid>",
  "expires_at": 1786400000,
  "can_speak": true, "can_publish_video": true, "can_share_screen": false }
```

- The LiveKit room name is exactly the TamizChat `room_id`.
- The participant identity is the `client_uuid`, so the server can target that
  person precisely.
- The token is valid for 15 minutes and is only needed at connect time. If you
  change rooms, get a new one.

### Permissions are genuinely enforced

The `can_*` flags exist so the client does not show a useless button. The token
itself carries the same restrictions (`canPublishSources` in the LiveKit grant),
so a client that ignores the flags is rejected by **LiveKit**, not by the user
interface.

Three permissions apply here: `speak` · `publish_video` · `share_screen`

Someone with none of them still gets a token — they can **listen and watch**, they
just cannot publish.

### Moderation applied to media

| Event | What the server does with LiveKit |
|-------|-------------------------------|
| Muted | `UpdateParticipant` with `canPublish=false` (current tracks are cut) |
| Unmuted / role changed | `UpdateParticipant` with the new permissions |
| Kicked or banned | `RemoveParticipant` |
| Leaving a room, moving, or disconnecting | `RemoveParticipant` |
| Room deleted | `DeleteRoom` |

If LiveKit is unreachable, these calls are merely logged and chat carries on.
**Media must never take the chat server down.**

### Microphone and camera state

The client sends `media.set_state`; the server broadcasts it to everyone and
keeps it in `User.media`, so the room tree can show microphone and camera icons.

This is the **client's own** report, because only the client knows whether its
camera is really on. Lying about it gains nothing but a wrong icon, since what
you are *allowed* to turn on is decided server-side and enforced by LiveKit.

Leaving a room resets this state.

**"Currently speaking" is deliberately not here:** it changes several times a
second and LiveKit gives every client the active speaker event directly. Use that,
not the TamizChat server.

### Operator setup

In the panel, under "Voice and video (LiveKit)":

- `livekit.url` — the address the client connects to, e.g. `ws://127.0.0.1:7880`.
  The server's API address is derived from it (`ws://` → `http://`).
- `livekit.api_key` and `livekit.api_secret` — the ones defined in the LiveKit
  config.
- Turn `livekit.enabled` on after setting those three.


## The paint board

A board is a list of **strokes** in the order they were drawn, kept in the room's
memory like chat and thrown away when the room empties.

### Strokes are streamed

A drawing should not appear all at once when the mouse is released, so every
stroke has three stages:

1. `paint.begin` — the server assigns an ID and broadcasts the stroke. The first
   points can ride along with this message, so a short gesture does not need two
   round trips.
2. `paint.append` — the following points, as the mouse moves.
3. `paint.end` — the end of the stroke.

`paint.append` has **no reply**: it is the most frequent message in the system and
the client has already drawn the point locally. Only errors produce a frame, and
one dropped point is not worth interrupting a drawing over.

The `Stroke` shape:

```json
{ "id": "…", "seq": 12, "room_id": "…", "author": "<client_uuid>",
  "tool": "pen", "color": "#ff0000", "width": 0.01,
  "points": [ {"x": 0.1, "y": 0.2} ], "done": false }
```

Tools: `pen` · `eraser` · `line` · `rect` · `ellipse`

### Coordinates are normalized

`x` and `y` are between 0 and 1, not pixels — otherwise the drawing lands in the
wrong place on a window at a different resolution. `width` is in the same space.

The server **discards** non-numeric coordinates (NaN and infinity) and clamps the
rest to the canvas edge; one NaN from one broken client can wreck the rendering
on every other client.

### Newcomers

Send `paint.state` to fetch the whole board. The server does not push it
automatically, because a busy board can be large and most users never open the
paint panel.

### Clearing and undo

- `paint.undo` removes **your own** last stroke, even if somebody else has drawn
  since.
- `paint.clear` with `scope: "mine"` erases only your work.
- `paint.clear` with `scope: "all"` erases the whole board and, because it
  destroys other people's work, requires the `moderate_chat` permission.

### Limits

- A cap on strokes per board (`paint.max_strokes`, 2000 by default). Once full,
  the server **rejects** a new stroke with `paint_board_full` — it does not drop
  the oldest, because silently erasing the beginning of a drawing is worse than
  saying no.
- A cap of 4000 points per stroke and 512 points per message.
- A separate rate limit for painting (`paint.rate_per_second` and
  `paint.rate_burst`).
- A muted user cannot draw: scribbling over everyone is the same nuisance as
  shouting.


## Bots

A music bot sits in a room like an ordinary participant and plays from a folder
of tracks. **The audio does not pass through this server**: LiveKit's Ingress
service fetches the file from an HTTP endpoint on this server, transcodes it, and
publishes it into the room itself. This server decides *what* plays and *where*,
not how the bytes move.

The `Bot` shape:

```json
{ "id": "…", "name": "DJ", "kind": "music", "color": "",
  "room_id": "…", "state": "playing",
  "track": { "index": 3, "title": "track name" },
  "track_count": 42, "loop": true, "shuffle": false, "enabled": true }
```

States: `idle` (in no room) · `stopped` (in a room, silent) · `playing`

Commands (`bot.control` → `action`): `play` · `stop` · `next` · `prev` ·
`select` (with `track_index`)

Any change to a bot's state broadcasts a `bot.state` to everyone — just like user
presence, because the client shows bots next to people in the same tree.

### Creating and deleting bots

`bot.create`, `bot.update` and `bot.delete` need the `manage_bots` permission,
which is separate from `control_bots` on purpose: driving the music is an
everyday job, configuring the server's bots is an administrative one.

The `BotSpec` shape — on an update every field is optional and an absent one is
left alone; a create needs at least a name:

```json
{ "bot_id": "…", "name": "DJ", "color": "#ff8800",
  "loop": true, "shuffle": false, "enabled": true }
```

**There is no folder field, and there will not be one.** The folder is a path on
the server's disk; a client naming one would turn bot creation into a way of
reading any directory on the host through the stream endpoint. A bot created
this way is given a folder of its own under the `bots.dir` setting, and its music
arrives by upload. A bot created from the CLI panel keeps pointing wherever the
operator pointed it.

Deleting a bot removes the folder the server made for it, and never touches a
folder an operator typed in themselves.

A create broadcasts an ordinary `bot.state`, so a client meeting an id it does
not know should add it rather than ignore the frame. A delete broadcasts
`bot.removed`. In both cases the actor is left out of the broadcast and matches
their own reply by `id`.

### Playlists

A bot has named playlists, and plays from one of them or from its own library.

```json
{ "id": "…", "bot_id": "…", "name": "Party", "track_count": 12 }
```

- The `Bot` shape carries `playlist_id` and `playlist_name` for whichever one is
  selected. Both are absent when the bot is on its own library — which is what a
  bot configured from the CLI panel is, always.
- **Selecting a playlist stops playback.** The queue is about to be a different
  list, and carrying the index across it would resume at an unrelated track.
- Deleting the active playlist puts the bot back on its library and stops it.
- Layout on disk, for the record: `<bots.dir>/<bot id>/default` is the library
  and `<bots.dir>/<bot id>/<playlist id>` is a playlist. The library is a folder
  *beside* the playlists, not above them: a scan walks subfolders, so a nested
  playlist would be played twice.

### Adding tracks

Uploading a track is the same two-step dance as a room file — **permission over
the socket, bytes over HTTP** — and for the same reason: everything that can be
refused is refused before a single byte is accepted.

1. `bot.track.upload_request` → `bot.track.upload_ticket`. The server checks the
   bot, the playlist, the file type and the bot's storage quota here.
2. `POST` the bytes to the ticket's `url`. The reply is the bot's new state.

The ticket is single-use and lives two minutes. Unlike a room file — stored
under a random id — a track keeps its **file name**: the queue is the folder
listing and the title a listener sees is that name. So the name is stripped of
anything that could leave the folder, must end in an accepted audio extension
(`.mp3 .ogg .opus .flac .m4a .aac .wav .wma`), and a second file by the same
name becomes "name (2).mp3" rather than replacing the first.

Unlike room files, this music is **permanent**: it is not purged when a room
empties. `bots.quota_mb` is the ceiling per bot and `bots.max_track_mb` the
ceiling for one track.

### What is deliberately missing

- **There is no pause.** With Ingress, stopping means closing the stream;
  resuming restarts the track from the **beginning**. A pause button that jumps
  out of the middle of a song is worse than no button. `stop` exists and is
  honest.
- **There is no server-side volume control.** Each listener sets a participant's
  volume in their own client (LiveKit supports this) — which is better anyway:
  everyone adjusts the bot for themselves, not for the whole room.

### Setup

1. Create the bot from the admin panel (option 10): a name and a music folder
   path. The panel says right there how many playable files it found, so a typo
   in the path is visible immediately.
2. Set `network.public_host`. Ingress has to fetch the file from this server, and
   the server cannot guess what address it is reachable at from outside.
3. LiveKit and its **Ingress** service must be up, and the LiveKit webhook must
   point at this server's `POST /api/v1/livekit/webhook`.

The file format does not matter (mp3, ogg, flac, m4a, …) — Ingress handles the
conversion.

### The webhook

`POST /api/v1/livekit/webhook` is the only way the server learns that a track
finished and the next one should start. LiveKit's signature (a JWT whose `sha256`
claim equals the body hash) is verified **before anything else happens**; this
endpoint has no other authentication, so an unsigned request changes nothing.

### The music file

`GET /api/v1/bot-stream/{bot_id}?token=…` has exactly one consumer: Ingress. The
token is single-use for that one playback and is invalidated by `stop`. The file
path never comes from the client — it is derived from the queue index and
re-checked to be inside the bot's configured folder.
