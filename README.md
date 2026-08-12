# TamizChat — backend

The self-hosted backend of the TamizChat project, written in Go.

## Running

```bash
go build -o tamizchat ./cmd/tamizchat
```

```bash
./tamizchat run
```

The server comes up on `:8080` and creates its database at `data/tamizchat.db`.

## Admin panel

With no arguments, the interactive panel opens (x-ui style):

```bash
./tamizchat
```

From inside the panel you can view and change the server name, password, port,
room settings, uploads, LiveKit and the log level. There is no `.env` file;
every setting lives in the same SQLite file.

> Panel changes reach a *running* server through the control socket — pick
> "Running server → Apply changes (reload)", or accept the prompt the panel
> offers after each edit. `network.listen_addr` is the one exception and still
> needs a restart.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-db` | `data/tamizchat.db` (or `TAMIZCHAT_DB`) | Path to the database file |
| `-log` | `info` | Initial log level: `debug\|info\|warn\|error` |

## Current endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Service health and uptime |
| GET | `/api/v1/server-info` | Public server info for the client, before connecting |
| GET | `/ws` | Client WebSocket connection (handshake with a `hello` message) |
| POST | `/api/v1/upload` | File upload with a ticket |
| GET | `/api/v1/file/{id}` | File download with a short-lived token |
| GET | `/api/v1/bot-stream/{id}` | A bot's music file (for LiveKit Ingress only) |
| POST | `/api/v1/livekit/webhook` | LiveKit webhook (signature-verified) |

## Tests

```bash
go test ./...
```

## Build

```bash
make build      # binary for this machine
make release    # linux/windows/macos, amd64 and arm64
make check      # vet + tests
```

## Documentation

- [Operator setup guide](docs/DEPLOY.md)
- [Setting up LiveKit without a domain or TLS](docs/LIVEKIT.md)

- [Client ↔ server protocol](docs/PROTOCOL.md)
- [Roadmap and phases](docs/ROADMAP.md)
- [Project memory and architecture decisions](MEMORY.md)
