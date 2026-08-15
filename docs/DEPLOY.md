# Setting up a TamizChat server

This guide is for whoever wants to bring the server up on their own machine.

## The shortest path

```bash
./tamizchat            # opens the admin panel
./tamizchat run        # runs the server
```

That is all. No `.env` file, no separate database, no extra service. The first
run creates `data/tamizchat.db` and everything lives inside it.

With no configuration at all, text chat, rooms, files and the paint board work.
Voice, video and the music bot need LiveKit (see below).

## Build

```bash
make build
```

The binary is built without CGo, so no shared library is needed on the server.
To build for every platform:

```bash
make release
```

## First things to do in the panel

Open the panel with `./tamizchat`:

1. **Option 2 → General server settings**: the server name and, if you want one,
   a password.
2. **Option 7 → Roles and permissions**: grant the "Admin" role to your own
   client ID. The client generates that ID at install time; connect once so it
   shows up under **option 5 (Known users)**, then grant the role.
3. **Option 6 → Rooms**: create a few rooms.

> Until you have given someone the admin role, nobody can create a room or kick
> a user from inside the app. That is deliberate.

## Running permanently

**Panel option 12** builds a systemd unit file with this installation's paths and
writes it if you have permission. Then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tamizchat
sudo systemctl status tamizchat
```

## Changing settings without downtime

The panel writes to the database and then tells the running server to re-read it.
After each edit it asks "apply now?" on the spot.

From **option 11** you can also trigger a `reload` by hand, see online users,
kick somebody, or broadcast a notice to everyone.

The one exception is `network.listen_addr`: the port is bound once at startup,
so changing it needs a restart.

## TLS

There are two ways, and **the second is recommended**:

### Behind a reverse proxy (recommended)

Put nginx or Caddy in front so TLS and certificate renewal are their problem. In
that case leave `tls.enabled` off and be sure to set:

```
network.trusted_proxies = 127.0.0.1
```

Without it, every connection looks to the server like it came from `127.0.0.1`,
and the per-IP connection cap treats the whole internet as one user.

An nginx example (mind `proxy_set_header Upgrade` — WebSocket does not work
without it):

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_read_timeout 3600s;   # sockets stay open for a long time
    client_max_body_size 64m;   # must exceed the upload limit
}
```

### Direct TLS

If nothing sits in front of the server, set the certificate and key paths in the
panel's **TLS** section and turn `tls.enabled` on. The server reads the
certificate at startup and fails loudly if it is broken, rather than quietly
coming up without TLS.

## Voice, video and screen sharing (LiveKit)

> Step-by-step guide with ready-made Docker files: **[LIVEKIT.md](LIVEKIT.md)**
> (including a no-domain, no-TLS setup)

The TamizChat server moves no audio or video packets; it only signs tokens and
applies moderation. Media is handled by **LiveKit**, which is a separate service.

1. Bring LiveKit up (Docker is the easiest way) and create an `api_key` and
   `api_secret`.
2. In the panel, under **Voice and video (LiveKit)**:
   - `livekit.url` → e.g. `ws://127.0.0.1:7880`
   - `livekit.api_key` and `livekit.api_secret`
   - then turn `livekit.enabled` on.

The server's API address is derived from that same `url` (`ws://` → `http://`).

## The music bot

Bots need LiveKit's **Ingress** service, because TamizChat itself does not
publish audio.

1. Bring `livekit-ingress` up alongside LiveKit.
2. Point the LiveKit webhook at this address:
   `https://<your host>/api/v1/livekit/webhook`
   Without it, the bot goes quiet after the first track finishes.
3. **You must set `network.public_host`** — for example
   `https://chat.example.com`. Ingress has to fetch the music file from your
   server, and the server cannot guess what address it is reachable at from
   outside.
4. Create the bot. Either from **panel option 10**, with a name and a music
   folder path — the panel tells you right there how many playable files it
   found — or from the client, by an administrator holding `manage_bots`.

The file format does not matter (mp3, ogg, flac, m4a, …) — Ingress handles the
conversion.

### Bots created from a client

A bot created from the client has no folder path: it is given storage of its own
under `bots.dir` (default `data/bots`), and an administrator fills it from the
client with **playlists** — named groups of tracks, uploaded over HTTP.

Two things follow, and both matter for an operator:

- **This music is permanent.** Unlike room files it survives a room emptying and
  a restart, so `bots.quota_mb` (per bot) and `bots.max_track_mb` (per track)
  are the settings to watch. Back up `bots.dir` alongside the database if the
  music matters — the database backup does not contain it.
- Deleting a bot from the client deletes its storage with it. A folder you typed
  into the panel yourself is never touched.

## Backups

**Panel option 13.** Backups are taken with `VACUUM INTO`, so they are **safe
while the server is running** — unlike a plain file copy, which can catch a
half-written page.

For an automatic nightly backup, a cron entry against the same file is enough:

```bash
0 4 * * * cd /opt/tamizchat && sqlite3 data/tamizchat.db \
  "VACUUM INTO 'data/backups/nightly-$(date +\%Y\%m\%d).db'"
```

Restoring from the panel is only possible while the server is **stopped**; the
current copy is not deleted and is kept alongside.

> What is in a backup: settings, rooms, roles, bans, bots, known users.
> What is not: chat, files, drawings — those are **deliberately** temporary and
> are erased when a room empties.

## What to open

| Port | For what |
|------|----------|
| the `listen_addr` port (default 8080) | clients |
| 7880 and LiveKit's UDP range | only if LiveKit runs on this same machine |

The panel's control channel is on **loopback** and must never be reachable from
outside.

## Hardening

The defaults are tuned for a friendly server. If yours is public:

- Set a **server password** (`server.password`).
- Lower `network.max_conns_per_ip` (default 8). If everyone is behind one NAT,
  set it to zero to turn it off.
- Lower `network.handshake_per_minute`.
- Tune `uploads.max_size_mb` and `uploads.room_quota_mb` to your disk.
- Run the server as a non-root user (the panel's systemd file does this).

## Troubleshooting

**A client cannot connect:** run `curl http://<host>:<port>/healthz`. If that
answers, the problem is the firewall or the reverse proxy — check `Upgrade` in
the nginx config.

**Voice does not work:** check whether `MediaOK` is on under panel option 11. If
not, one of the three LiveKit values is empty.

**The bot plays only one track:** the LiveKit webhook is not reaching the server.

**The bot plays nothing at all:** check `network.public_host`.

**More logging:** set `log.level` to `debug` — it applies without a restart.
