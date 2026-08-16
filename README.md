# TamizChat Server

The server behind TamizChat: rooms, text chat, files, a shared paint board,
roles and moderation, voice/video, and music bots — in a single Go binary with a
SQLite database beside it.

There is **no** `.env`, no separate database server, no Redis, and no config file
to edit by hand. Everything is configured from an interactive admin panel that
the same binary opens when you run it with no arguments.

*[راهنمای فارسی](README.fa.md)*

---

## Contents

- [Requirements](#requirements)
- [Install](#install)
- [First run](#first-run)
- [Running permanently (systemd)](#running-permanently-systemd)
- [Behind a domain, without TLS](#behind-a-domain-without-tls)
- [With TLS](#with-tls)
- [Voice and video (LiveKit)](#voice-and-video-livekit)
- [Music bots](#music-bots)
- [Backups](#backups)
- [Settings reference](#settings-reference)
- [Ports](#ports)
- [Troubleshooting](#troubleshooting)

---

## Requirements

| | |
|---|---|
| OS | Linux, Windows or macOS (Linux for a real server) |
| CPU/RAM | Anything. A small VPS runs it comfortably |
| Go | 1.24+, **only if building from source** |
| Docker | Optional, only for LiveKit |

The binary is built without CGo, so there is nothing to install alongside it —
no libc version to match, no SQLite package.

## Install

### From a release

```bash
wget https://github.com/<you>/tamizchat-server/releases/latest/download/tamizchat-linux-amd64
chmod +x tamizchat-linux-amd64
sudo mv tamizchat-linux-amd64 /usr/local/bin/tamizchat
```

### From source

```bash
git clone https://github.com/<you>/tamizchat-server.git
cd tamizchat-server
make build          # or: go build -o tamizchat ./cmd/tamizchat
```

Every platform at once:

```bash
make release        # linux/windows, amd64/arm64, into dist/
```

> **`403 Forbidden` while downloading modules?** `proxy.golang.org` and
> `sum.golang.org` are Google services and are blocked from some countries. Point
> Go at a mirror once, and the build works:
>
> ```bash
> go env -w GOPROXY=https://goproxy.io,direct
> go env -w GOSUMDB=off
> ```
>
> `goproxy.cn` or `mirrors.aliyun.com/goproxy/` are alternatives if that one is
> slow. Keep the `,direct` at the end.

## First run

```bash
tamizchat            # opens the admin panel
tamizchat run        # runs the server
```

The first run creates `data/tamizchat.db` in the working directory. Everything
lives in there: settings, rooms, roles, bans, bots and known users.

In the panel, do these three things in order:

**1 — Name the server.** Option `2 → General`. Set the server name, and a
password if you do not want the server to be open.

**2 — Make yourself an administrator.** Nobody can create a room or kick anyone
until somebody holds the Admin role, and that first grant has to happen here on
the machine — by design.

- Start the server (`tamizchat run`) and connect once with the client, so your
  client ID becomes known.
- In the panel: option `5` (Known users) shows it.
- Option `7 → 5` (Grant a role to a user) → pick **Admin** → pick yourself.

> A role granted while the server is running is applied immediately; the panel
> asks "apply now?" after each change.

**3 — Create rooms.** Option `6 → n`.

## Running permanently (systemd)

Panel option `12` writes a unit file for **this** installation, with its real
paths already filled in and hardened (`ProtectSystem`, `ReadWritePaths`, a
non-root user). Then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tamizchat
sudo systemctl status tamizchat
```

Logs:

```bash
journalctl -u tamizchat -f
```

## Behind a domain, without TLS

This is the common case: you own `chat.example.com`, its A record points at the
server, and you do not want certificates yet. **It works, and here is exactly how
to set it up** — plus what you are accepting by doing it.

> **What you are accepting.** Without TLS, everything travels in the clear:
> messages, the server password, and files. Anyone between the user and the
> server — their ISP, a public Wi-Fi, a hosting provider — can read and change
> it. Browsers and some networks also block plain WebSocket traffic. Use this on
> a private or trusted network, and [turn TLS on](#with-tls) before letting
> strangers in. It is fifteen minutes of work and free.

### The short way: no proxy at all

Run TamizChat directly on port 80 and point the domain at it.

```bash
tamizchat
```

- Option `2 → 1` **Listen address** → `:80`
- Option `2 → 2` **Public host** → `http://chat.example.com`

A port below 1024 needs permission. Give the binary the capability rather than
running it as root:

```bash
sudo setcap 'cap_net_bind_service=+ep' /usr/local/bin/tamizchat
sudo systemctl restart tamizchat
```

Open the port:

```bash
sudo ufw allow 80/tcp
```

Clients then connect to `chat.example.com` (or `ws://chat.example.com/ws`), with
no port to type.

**The listen address is the one setting a reload does not apply** — the port is
bound once at startup, so restart the service after changing it.

### The better way: nginx in front, still without TLS

Worth it if you also serve something else from the same machine, or expect to
add TLS later — then it is one `certbot` command away.

```bash
sudo apt install nginx
```

`/etc/nginx/sites-available/tamizchat`:

```nginx
server {
    listen 80;
    server_name chat.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        # Without these two, the WebSocket never upgrades and nothing works.
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Sockets stay open for hours; the default would cut them at 60s.
        proxy_read_timeout 3600s;

        # Must exceed uploads.max_size_mb, or large files fail at nginx.
        client_max_body_size 64m;
    }
}
```

```bash
sudo ln -s /etc/nginx/sites-available/tamizchat /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

Then in the panel:

- Option `2 → 1` **Listen address** → `127.0.0.1:8080` (only nginx reaches it)
- Option `2 → 2` **Public host** → `http://chat.example.com`
- Option `2 → 7` **Trusted proxies** → `127.0.0.1`

> **Set the trusted proxies.** Without it every connection looks like it came
> from `127.0.0.1`, so the per-IP connection limit treats the whole internet as
> one user: one noisy client can lock everybody else out.

Restart, and you are done:

```bash
sudo systemctl restart tamizchat
```

### Adding TLS later

Nothing above is wasted:

```bash
sudo apt install certbot python3-certbot-nginx
sudo certbot --nginx -d chat.example.com
```

Then change **Public host** to `https://chat.example.com`. Certbot rewrites the
nginx config itself; TamizChat still listens on plain HTTP behind it.

## With TLS

Two ways, and the first is recommended.

**Behind a reverse proxy** — as above, with `certbot`. Leave `tls.enabled` off:
nginx terminates TLS and TamizChat stays on loopback.

**Directly in TamizChat** — when nothing sits in front of it. Panel option
`2 → 3` (TLS): set the certificate and key paths and turn `tls.enabled` on. The
certificate is checked at startup, and the server refuses to start on a broken
one rather than quietly coming up without TLS.

## Voice and video (LiveKit)

TamizChat moves no audio or video itself. It signs tokens and applies moderation;
**LiveKit** carries the media. Text chat, rooms, files and the paint board all
work without it.

1. Run LiveKit (see [docs/LIVEKIT.md](docs/LIVEKIT.md) for ready-made Docker
   files, including a setup with no domain and no TLS).
2. Panel option `2 → 9` (Voice and video):
   - `livekit.url` → e.g. `ws://127.0.0.1:7880`
   - `livekit.api_key` and `livekit.api_secret`
   - turn `livekit.enabled` on

The server's API address is derived from the same URL (`ws://` → `http://`).

A user's rights in LiveKit mirror their TamizChat permissions exactly, so a
client that lies about what it may publish is refused by LiveKit itself.

## Music bots

A bot joins a room as a participant of its own and plays a playlist, controlled
from the client by anyone with the `control_bots` permission.

- Bots need **LiveKit** (above). Nothing else — no Ingress, no Redis.
- Tracks are stored as **Ogg/Opus**; the client converts whatever you upload
  (mp3, m4a, wav, flac) before sending it. The server publishes those packets
  untouched, so it never decodes audio.
- An administrator with `manage_bots` creates bots and fills playlists from the
  client's admin panel; option `10` in the CLI panel covers the same ground.
- The music is **permanent** — unlike room files it survives restarts — so watch
  `bots.quota_mb` (per bot) and back up `bots.dir` alongside the database.

## Backups

Panel option `13`. Backups use `VACUUM INTO`, so they are **safe while the server
is running** — unlike copying the file, which can catch a half-written page.

A nightly cron entry against the same database:

```bash
0 4 * * * cd /opt/tamizchat && sqlite3 data/tamizchat.db \
  "VACUUM INTO 'data/backups/nightly-$(date +\%Y\%m\%d).db'"
```

Restoring is only possible while the server is **stopped**, and the current
database is kept rather than deleted.

**In a backup:** settings, rooms, roles, bans, bots, known users.
**Not in a backup:** chat, files and drawings — those are deliberately temporary
and are erased when a room empties. Bot music is on disk, not in the database.

## Settings reference

Everything below is edited in the panel (option `2`), stored in the database, and
applied live — except the listen address.

| Section | What it covers |
|---|---|
| `server` | Name, welcome message, password, maximum users |
| `network` | Listen address, public host, heartbeat, per-IP limits, trusted proxies |
| `tls` | Certificate and key, on/off |
| `users` | Username length |
| `rooms` | Room count, default capacity, history size, purge on empty |
| `chat` | Message length, rate limits, stickers |
| `uploads` | On/off, size limit, per-room quota, storage folder |
| `paint` | On/off, stroke limits, rate limits |
| `livekit` | URL, API key and secret, on/off |
| `bots` | Storage folder, per-track and per-bot size limits |
| `backup` | Folder, how many to keep |
| `log` | Level (changes at runtime) |

Option `3` prints every setting with its current value; option `4` resets one to
its default.

## Ports

| Port | For | Needed when |
|---|---|---|
| 80 or 443 | Clients | Always (or 8080 if you use no proxy and no domain) |
| 7880/tcp | LiveKit signalling | Voice/video |
| 7881/tcp | LiveKit ICE over TCP | Voice/video fallback |
| 50000-60000/udp | LiveKit media | Voice/video |

The panel's control channel listens on **loopback only** with a token that
changes every run; it must never be reachable from outside.

## Troubleshooting

**A client cannot connect.** `curl http://<host>/healthz` — if that answers, the
problem is the firewall or the proxy. In nginx, check the two `Upgrade` headers.

**Voice does not work.** Panel option `11` shows `MediaOK`. If it is off, one of
the three LiveKit settings is empty or LiveKit is unreachable.

**A music bot says it is playing but nobody hears it.** Almost always LiveKit:
check that voice works for people first. The server logs each track it serves at
debug level (`tamizchat run -log debug`).

**Everyone appears to come from 127.0.0.1.** `network.trusted_proxies` is not
set — see [above](#the-better-way-nginx-in-front-still-without-tls).

**A setting changed but nothing happened.** Only `network.listen_addr` needs a
restart. Everything else applies when the panel asks "apply now?", or from option
`11 → reload`.

---

## Documentation

- [docs/PROTOCOL.md](docs/PROTOCOL.md) — the full client/server wire protocol
- [docs/DEPLOY.md](docs/DEPLOY.md) — the operator guide in more depth
- [docs/LIVEKIT.md](docs/LIVEKIT.md) — LiveKit, step by step
- [docs/ROADMAP.md](docs/ROADMAP.md) — what was built, phase by phase
