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

Only for the **music bot**. Voice chat, video chat and screen sharing all work
without it. Recommendation: get it running without Ingress first, then add it if
you want the bot. One less service is one less thing to break.

---

## Step 1 — generate keys

```bash
docker run --rm livekit/livekit-server generate-keys
```

It prints two lines: an `API...` key and a secret. Keep both — you need them in
three places: `livekit.yaml`, `ingress.yaml` (if you want the bot) and the
TamizChat panel.

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

## Adding the music bot (optional)

1. Uncomment the `redis` section in `livekit.yaml`.
2. Put the key and secret in `ingress.yaml`.
3. Bring it up:

```bash
docker compose --profile bots up -d
curl http://127.0.0.1:8086     # Ingress health
```

4. In the TamizChat panel you **must** set:

```
network.public_host = http://<server ip>:8080
```

Without it the bot plays nothing. The reason: Ingress has to **fetch** the music
file from your server, and the server cannot guess what address it is reachable
at from outside.

5. Configure the LiveKit webhook. Add this to `livekit.yaml`:

```yaml
webhook:
  api_key: APIchangeme          # the same key
  urls:
    - http://<server ip>:8080/api/v1/livekit/webhook
```

Without this, the bot **goes quiet after the first track** — the server never
learns that the track ended and that it should start the next one.

6. Create the bot from **panel option 10** with a name and a music folder path.

---

## Troubleshooting

**Connects but there is no audio** → the UDP port is closed, or
`use_external_ip` is announcing the wrong address. Set `log_level: debug` and
read the LiveKit log.

**The app does not reach media at all** → test `livekit.url` from the client's
own machine: `curl http://<ip>:7880`.

**The bot plays only one track** → the webhook is not configured (step 5 above).

**The bot plays nothing at all** → `network.public_host` is empty, or Ingress
cannot reach that address. Test from inside the container:
`docker exec tamizchat-ingress wget -qO- http://<server ip>:8080/healthz`

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
