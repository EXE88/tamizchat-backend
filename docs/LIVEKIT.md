# Setting up LiveKit for TamizChat (no domain, no TLS)

This guide covers the case where you and a few friends want everything — voice
and video included — running on one server, with nothing but an IP address.

## Can this really work without TLS?

**Yes.** The TamizChat client is a desktop app, not a browser. The famous
"microphone requires HTTPS" rule belongs to browsers. A desktop app connects to
`ws://<ip>:7880` happily, and WebRTC works without TLS.

The one place you hit a wall: testing with a **browser** (like LiveKit Meet).
There you get no microphone without HTTPS, except on `localhost`.

> If you ever put the server on the public internet with strangers on it, add
> TLS — not because of WebRTC (which is encrypted anyway) but because of the
> signalling and the server password, which travel in the clear over plain
> `ws://`.

## Do you need Ingress?

**No.** Earlier versions of TamizChat played the music bot through LiveKit's
Ingress service, which also needed Redis and a webhook. That is gone: the bot now
joins the room as an ordinary participant and the server publishes the audio
itself. You need **only LiveKit** — nothing else — for voice, video, screen
sharing *and* the music bot.

If you find `ingress.yaml` or a `redis` section in the deploy files, they are
leftovers from the old design and are not used.

---

## Step 1 — generate keys

```bash
docker run --rm livekit/livekit-server generate-keys
```

It prints two lines: an `API...` key and a secret. Keep both — you need them in
two places: `livekit.yaml` and the TamizChat panel.

If you do not have Docker:

```bash
echo "API$(openssl rand -hex 6)"
openssl rand -base64 32
```

## Step 2 — fill in the config

In the `deploy/` folder:

- In `livekit.yaml`, replace the `keys` section with your own key and secret.
- If the server is **at home behind a router**, read the `use_external_ip`
  section and set `node_ip` by hand as it explains.

## Step 3 — bring LiveKit up

```bash
cd deploy
docker compose up -d
docker compose logs -f livekit
```

The log should show it listening on port 7880. Test it:

```bash
curl http://127.0.0.1:7880
```

An `OK` reply means it is up.

### Without Docker

If you would rather not use Docker:

```bash
curl -sSL https://get.livekit.io | bash
livekit-server --config livekit.yaml
```

Then take the systemd unit file the TamizChat panel generates as a template and
write one for LiveKit too.

## Step 4 — open the ports

| Port | Protocol | For what |
|------|----------|----------|
| 8080 | TCP | TamizChat itself (clients) |
| 7880 | TCP | LiveKit signalling |
| 7882 | **UDP** | audio and video |
| 7881 | TCP | fallback path when UDP is blocked |

```bash
sudo ufw allow 8080/tcp
sudo ufw allow 7880/tcp
sudo ufw allow 7881/tcp
sudo ufw allow 7882/udp
```

On a home server, forward those same four on the router as well.

> **The UDP port matters most.** Without it the connection is established but
> there is no audio, or it arrives late over the TCP fallback.

## Step 5 — tell TamizChat about it

Open the panel (`./tamizchat`) → **option 2 → Voice and video (LiveKit)**:

| Setting | Value |
|---------|-------|
| `livekit.url` | `ws://<server ip>:7880` |
| `livekit.api_key` | the `API...` key |
| `livekit.api_secret` | the secret |
| `livekit.enabled` | `true` — **last of all** |

The address must be the one **clients** use to reach the server, not
`127.0.0.1`. If your friends connect over the internet, use the public IP; over
a LAN, the local one.

After saving, the panel asks "apply now?" — say yes.

## Step 6 — test it

In the panel, **option 11 → Live status**. You should see:

```
Voice/video   : enabled
```

If it says `disabled`, one of those three values is still empty.

---

## The music bot

Nothing to set up here. Once LiveKit works (the steps above), the bot works — no
Ingress, no Redis, no webhook, and no `network.public_host` to configure.

Create and fill bots from the **client's admin panel** (an administrator with the
`manage_bots` permission), or a folder-based bot from **panel option 10**. Tracks
you upload from the client are converted to Ogg/Opus by the client and published
by the server as-is; the server never fetches anything or decodes audio.

---

## Troubleshooting

**Connects but there is no audio** → the UDP port is closed, or
`use_external_ip` is announcing the wrong address. Set `log_level: debug` and
read the LiveKit log.

**The app does not reach media at all** → test `livekit.url` from the client's
own machine: `curl http://<ip>:7880`.

**The bot says it is playing but nobody hears it** → this is a LiveKit media
problem, not a bot one. Check that ordinary voice works for people in the room
first; if voice is silent too, it is the UDP port or `use_external_ip` above.
Run the server with `tamizchat run -log debug` to see each track it publishes.

---

## An honest warning

The files in `deploy/` were written against the documented shape of the LiveKit
config, but **they have not been run or tested on this machine** — LiveKit is not
installed here. Key names occasionally move between LiveKit versions.

If LiveKit complains about a key when you bring it up, compare against the sample
config for the exact version you pulled:

```bash
docker run --rm livekit/livekit-server --help
```
