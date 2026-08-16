#!/usr/bin/env bash
#
# TamizChat server installer for Linux.
#
# It does every step an operator would otherwise do by hand: obtain the binary,
# create a system user, lay out /opt/tamizchat, write a hardened systemd unit,
# seed the settings that live in SQLite, and optionally put nginx (with TLS) or
# LiveKit in front of it.
#
# Run it with no arguments for a guided install:
#
#   sudo ./install.sh
#
# Or non-interactively, e.g.:
#
#   sudo ./install.sh --yes --name "My Server" --domain chat.example.com \
#        --nginx --tls --email me@example.com
#
# See --help for everything.

set -euo pipefail

# --- constants ---------------------------------------------------------------

APP_NAME="tamizchat"
SVC_USER="tamizchat"
APP_DIR="/opt/tamizchat"
DATA_DIR="$APP_DIR/data"
DB_PATH="$DATA_DIR/tamizchat.db"
BIN_PATH="/usr/local/bin/tamizchat"
PANEL_PATH="/usr/local/bin/tamizchat-panel"
UNIT_PATH="/etc/systemd/system/tamizchat.service"
LIVEKIT_DIR="$APP_DIR/livekit"
NGINX_SITE="/etc/nginx/sites-available/tamizchat"
NGINX_LINK="/etc/nginx/sites-enabled/tamizchat"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- options (overridable from the command line) -----------------------------

ASSUME_YES=0
UNINSTALL=0
PURGE=0

OPT_BINARY=""
OPT_SOURCE=""
OPT_URL=""

OPT_NAME=""
OPT_PASSWORD=""
OPT_LISTEN=""
OPT_DOMAIN=""
OPT_EMAIL=""

USE_NGINX=-1     # -1 ask, 0 no, 1 yes
USE_TLS=-1
USE_LIVEKIT=-1
USE_FIREWALL=-1
USE_BACKUP_CRON=-1

# --- output helpers ----------------------------------------------------------

if [ -t 1 ]; then
  B=$'\033[1m'; R=$'\033[0m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; DIM=$'\033[2m'
else
  B=""; R=""; GREEN=""; YELLOW=""; RED=""; DIM=""
fi

step()  { printf '\n%s==>%s %s%s%s\n' "$GREEN" "$R" "$B" "$*" "$R"; }
info()  { printf '    %s\n' "$*"; }
note()  { printf '    %s%s%s\n' "$DIM" "$*" "$R"; }
warn()  { printf '%s !! %s%s\n' "$YELLOW" "$*" "$R"; }
die()   { printf '%s !! %s%s\n' "$RED" "$*" "$R" >&2; exit 1; }

# The install stops the service for a while to seed its settings. If the script
# ends before starting it again — an error, a Ctrl+C — the server must still
# come back up, and the temporary SQL (which holds secrets) must go.
NEED_START=0
SQL_FILE=""
LIST_FILE=""
cleanup() {
  [ -n "$SQL_FILE" ] && rm -f "$SQL_FILE"
  [ -n "$LIST_FILE" ] && rm -f "$LIST_FILE"
  if [ "$NEED_START" = 1 ]; then
    printf '\n    starting %s again before leaving\n' "$APP_NAME"
    systemctl start "$APP_NAME" >/dev/null 2>&1 || true
  fi
  return 0
}
trap cleanup EXIT INT TERM

# ask "Question" "default"  -> echoes the answer
ask() {
  local prompt="$1" default="${2:-}" reply
  if [ "$ASSUME_YES" = 1 ]; then
    printf '%s' "$default"
    return
  fi
  if [ -n "$default" ]; then
    read -r -p "    $prompt [$default]: " reply </dev/tty || reply=""
  else
    read -r -p "    $prompt: " reply </dev/tty || reply=""
  fi
  printf '%s' "${reply:-$default}"
}

# ask_secret "Question" -> echoes the answer, no echo on screen
ask_secret() {
  local prompt="$1" reply=""
  if [ "$ASSUME_YES" = 1 ]; then
    printf ''
    return
  fi
  read -r -s -p "    $prompt: " reply </dev/tty || reply=""
  printf '\n' >&2
  printf '%s' "$reply"
}

# confirm "Question" "Y|N"  -> returns 0 for yes
confirm() {
  local prompt="$1" default="${2:-Y}" reply hint
  if [ "$ASSUME_YES" = 1 ]; then
    [ "$default" = "Y" ]
    return
  fi
  [ "$default" = "Y" ] && hint="[Y/n]" || hint="[y/N]"
  read -r -p "    $prompt $hint " reply </dev/tty || reply=""
  reply="${reply:-$default}"
  case "$reply" in [yY]*) return 0 ;; *) return 1 ;; esac
}

usage() {
  cat <<'EOF'
TamizChat server installer (Linux)

Usage:
  sudo ./install.sh [options]

Where the binary comes from (the first one that applies wins):
  --binary PATH       use this prebuilt binary
  --source DIR        build from a source checkout with Go (default: this repo)
  --url URL           download the binary from this URL

Server settings (all optional; anything omitted is asked for, or left at its
default in non-interactive mode):
  --name NAME         server name shown to clients
  --password PASS     server password ("" = open server)
  --listen ADDR       listen address (default :8080, or 127.0.0.1:8080 with nginx)
  --domain HOST       the domain clients will use, e.g. chat.example.com
  --email ADDR        e-mail for Let's Encrypt

Extras:
  --nginx / --no-nginx        reverse proxy in front (recommended with a domain)
  --tls / --no-tls            Let's Encrypt certificate via certbot (needs nginx)
  --livekit / --no-livekit    install LiveKit with Docker for voice and video
  --firewall / --no-firewall  open the needed ports in ufw/firewalld
  --backup-cron / --no-backup-cron   nightly database backup at 04:00

Other:
  -y, --yes           non-interactive: accept every default, ask nothing
  --uninstall         remove the service, the binary and the nginx site
  --purge             with --uninstall, also delete /opt/tamizchat and the user
  -h, --help          this text

Examples:
  sudo ./install.sh
  sudo ./install.sh --yes --name "Friends" --listen :8080
  sudo ./install.sh --yes --domain chat.example.com --nginx --tls --email me@x.com
  sudo ./install.sh --uninstall --purge
EOF
}

# --- argument parsing --------------------------------------------------------

while [ $# -gt 0 ]; do
  case "$1" in
    --binary)   OPT_BINARY="${2:-}"; shift 2 ;;
    --source)   OPT_SOURCE="${2:-}"; shift 2 ;;
    --url)      OPT_URL="${2:-}"; shift 2 ;;
    --name)     OPT_NAME="${2:-}"; shift 2 ;;
    --password) OPT_PASSWORD="${2:-}"; shift 2 ;;
    --listen)   OPT_LISTEN="${2:-}"; shift 2 ;;
    --domain)   OPT_DOMAIN="${2:-}"; shift 2 ;;
    --email)    OPT_EMAIL="${2:-}"; shift 2 ;;
    --nginx)    USE_NGINX=1; shift ;;
    --no-nginx) USE_NGINX=0; shift ;;
    --tls)      USE_TLS=1; shift ;;
    --no-tls)   USE_TLS=0; shift ;;
    --livekit)  USE_LIVEKIT=1; shift ;;
    --no-livekit) USE_LIVEKIT=0; shift ;;
    --firewall) USE_FIREWALL=1; shift ;;
    --no-firewall) USE_FIREWALL=0; shift ;;
    --backup-cron) USE_BACKUP_CRON=1; shift ;;
    --no-backup-cron) USE_BACKUP_CRON=0; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --purge)    PURGE=1; shift ;;
    -y|--yes)   ASSUME_YES=1; shift ;;
    -h|--help)  usage; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

# --- system detection --------------------------------------------------------

require_root() {
  [ "$(id -u)" = "0" ] || die "run this as root: sudo $0 $*"
}

PKG=""
detect_pkg() {
  if   command -v apt-get >/dev/null 2>&1; then PKG=apt
  elif command -v dnf     >/dev/null 2>&1; then PKG=dnf
  elif command -v yum     >/dev/null 2>&1; then PKG=yum
  elif command -v pacman  >/dev/null 2>&1; then PKG=pacman
  elif command -v zypper  >/dev/null 2>&1; then PKG=zypper
  else PKG=""; fi
}

APT_UPDATED=0
pkg_install() {
  [ $# -gt 0 ] || return 0
  case "$PKG" in
    apt)
      if [ "$APT_UPDATED" = 0 ]; then
        DEBIAN_FRONTEND=noninteractive apt-get update -qq || true
        APT_UPDATED=1
      fi
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" ;;
    dnf)    dnf install -y "$@" ;;
    yum)    yum install -y "$@" ;;
    pacman) pacman -Sy --noconfirm "$@" ;;
    zypper) zypper --non-interactive install "$@" ;;
    *)      warn "no known package manager; install these by hand: $*"; return 1 ;;
  esac
}

need_cmd() {
  # need_cmd <command> <package...> - package names differ per distribution, so
  # try them together first and then one by one.
  local cmd="$1"; shift
  command -v "$cmd" >/dev/null 2>&1 && return 0
  info "installing $cmd"
  pkg_install "$@" >/dev/null 2>&1 || true
  if ! command -v "$cmd" >/dev/null 2>&1; then
    local p
    for p in "$@"; do pkg_install "$p" >/dev/null 2>&1 || true; done
  fi
  command -v "$cmd" >/dev/null 2>&1
}

goarch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) echo "" ;;
  esac
}

# --- uninstall ---------------------------------------------------------------

do_uninstall() {
  require_root
  step "Removing TamizChat"

  if systemctl list-unit-files | grep -q '^tamizchat\.service'; then
    systemctl disable --now "$APP_NAME" >/dev/null 2>&1 || true
    info "service stopped and disabled"
  fi
  rm -f "$UNIT_PATH" && systemctl daemon-reload || true
  rm -f "$BIN_PATH" "$PANEL_PATH"
  rm -f /etc/cron.d/tamizchat-backup

  if [ -L "$NGINX_LINK" ] || [ -f "$NGINX_SITE" ]; then
    rm -f "$NGINX_LINK" "$NGINX_SITE"
    command -v nginx >/dev/null 2>&1 && { nginx -t >/dev/null 2>&1 && systemctl reload nginx || true; }
    info "nginx site removed"
  fi

  if [ -f "$LIVEKIT_DIR/docker-compose.yml" ] && command -v docker >/dev/null 2>&1; then
    (cd "$LIVEKIT_DIR" && docker compose down >/dev/null 2>&1) || true
    info "LiveKit container stopped"
  fi

  if [ "$PURGE" = 1 ]; then
    rm -rf "$APP_DIR"
    id -u "$SVC_USER" >/dev/null 2>&1 && userdel "$SVC_USER" >/dev/null 2>&1 || true
    info "$APP_DIR and the $SVC_USER user are gone"
  else
    info "$APP_DIR kept (use --purge to delete the data as well)"
  fi

  step "Done"
  exit 0
}

[ "$UNINSTALL" = 1 ] && do_uninstall

# --- 1. preflight ------------------------------------------------------------

require_root
detect_pkg

cat <<EOF

${B}TamizChat server installer${R}
${DIM}rooms, chat, files, paint board, roles, voice/video and music bots${R}

EOF

command -v systemctl >/dev/null 2>&1 || die "systemd is required (this script installs a service)"
[ -n "$PKG" ] || warn "unknown distribution: missing packages will have to be installed by hand"

step "Checking the basics"
need_cmd curl curl || warn "curl is missing; some steps may not work"
need_cmd sqlite3 sqlite3 || warn "sqlite3 is missing"
info "package manager: ${PKG:-unknown}   architecture: $(uname -m)"

# The settings are seeded straight into the database with the sqlite3 CLI, and
# the tables the server creates are STRICT — a table option SQLite only learned
# in 3.37. On an older CLI the seeding is skipped and the values are printed for
# the panel instead, which is a working install either way.
SKIP_SEED=0
SQLITE_VER="$(sqlite3 -version 2>/dev/null | awk '{print $1}')"
if [ -z "$SQLITE_VER" ]; then
  SKIP_SEED=1
else
  sv_major="${SQLITE_VER%%.*}"; sv_rest="${SQLITE_VER#*.}"; sv_minor="${sv_rest%%.*}"
  case "$sv_major$sv_minor" in
    *[!0-9]*) SKIP_SEED=1 ;;
    *) if [ "$sv_major" -lt 3 ] || { [ "$sv_major" -eq 3 ] && [ "$sv_minor" -lt 37 ]; }; then
         SKIP_SEED=1
       fi ;;
  esac
  info "sqlite3 $SQLITE_VER"
fi
[ "$SKIP_SEED" = 1 ] && warn "sqlite3 is too old to write the settings; they will be printed for the panel instead"

# --- 2. get the binary -------------------------------------------------------

step "The TamizChat binary"

STAGED_BIN=""
ARCH="$(goarch)"

build_from_source() {
  local src="$1"
  [ -f "$src/go.mod" ] || return 1
  if ! command -v go >/dev/null 2>&1; then
    confirm "Go is not installed. Install it now?" Y || return 1
    pkg_install golang-go || pkg_install go || pkg_install golang || return 1
  fi
  command -v go >/dev/null 2>&1 || return 1

  info "building from source in $src (this takes a minute)"
  local out="/tmp/tamizchat-build.$$"
  if ! (cd "$src" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$out" ./cmd/tamizchat); then
    warn "the build failed."
    warn "if it was a 403 from proxy.golang.org, point Go at a mirror and retry:"
    note "  go env -w GOPROXY=https://goproxy.io,direct"
    note "  go env -w GOSUMDB=off"
    return 1
  fi
  STAGED_BIN="$out"
}

if [ -n "$OPT_BINARY" ]; then
  [ -f "$OPT_BINARY" ] || die "no such file: $OPT_BINARY"
  STAGED_BIN="$OPT_BINARY"
  info "using $OPT_BINARY"
elif [ -n "$OPT_URL" ]; then
  STAGED_BIN="/tmp/tamizchat-download.$$"
  info "downloading $OPT_URL"
  curl -fsSL -o "$STAGED_BIN" "$OPT_URL" || die "download failed"
elif [ -n "$OPT_SOURCE" ]; then
  build_from_source "$OPT_SOURCE" || die "could not build from $OPT_SOURCE"
elif [ -n "$ARCH" ] && [ -f "$SCRIPT_DIR/../dist/tamizchat-linux-$ARCH" ]; then
  STAGED_BIN="$SCRIPT_DIR/../dist/tamizchat-linux-$ARCH"
  info "using the prebuilt binary next to this script: $STAGED_BIN"
elif [ -f "$SCRIPT_DIR/../go.mod" ]; then
  build_from_source "$SCRIPT_DIR/.." || die "could not build the binary"
else
  die "no binary found. Pass --binary FILE, --url URL or --source DIR."
fi

chmod +x "$STAGED_BIN"
"$STAGED_BIN" version >/dev/null 2>&1 || die "$STAGED_BIN does not run on this machine (wrong architecture?)"
info "binary works: $("$STAGED_BIN" version 2>/dev/null || echo tamizchat)"

# --- 3. user and layout ------------------------------------------------------

step "User and folders"

if ! id -u "$SVC_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$APP_DIR" --shell /usr/sbin/nologin "$SVC_USER" 2>/dev/null \
    || useradd --system --home-dir "$APP_DIR" --shell /sbin/nologin "$SVC_USER"
  info "created the system user '$SVC_USER'"
else
  info "the user '$SVC_USER' already exists"
fi

mkdir -p "$DATA_DIR"
# A running binary cannot be overwritten in place (ETXTBSY), so an upgrade stops
# the service first; it is started again at the end.
systemctl is-active --quiet "$APP_NAME" 2>/dev/null && systemctl stop "$APP_NAME"
install -m 0755 "$STAGED_BIN" "$BIN_PATH"
chown -R "$SVC_USER:$SVC_USER" "$APP_DIR"
chmod 0750 "$APP_DIR" "$DATA_DIR"
info "binary:   $BIN_PATH"
info "data:     $DATA_DIR   ${DIM}(database, uploads, bot music, backups)${R}"

# a wrapper so the operator never has to remember the user or the -db path
cat > "$PANEL_PATH" <<EOF
#!/usr/bin/env bash
# Opens the TamizChat admin panel against the installed database.
#
# The panel must run as the service user: the database and the running server's
# control socket both belong to it.
set -eu
if [ "\$(id -un)" = "$SVC_USER" ]; then
  exec $BIN_PATH -db $DB_PATH "\$@"
elif [ "\$(id -u)" = "0" ] && command -v runuser >/dev/null 2>&1; then
  exec runuser -u $SVC_USER -- $BIN_PATH -db $DB_PATH "\$@"
elif command -v sudo >/dev/null 2>&1; then
  exec sudo -u $SVC_USER $BIN_PATH -db $DB_PATH "\$@"
else
  exec su -s /bin/sh $SVC_USER -c "$BIN_PATH -db $DB_PATH \$*"
fi
EOF
chmod 0755 "$PANEL_PATH"
info "panel:    $PANEL_PATH   ${DIM}(run 'tamizchat-panel')${R}"

# --- 4. systemd --------------------------------------------------------------

step "systemd service"

cat > "$UNIT_PATH" <<EOF
[Unit]
Description=TamizChat server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_USER
WorkingDirectory=$APP_DIR
ExecStart=$BIN_PATH run -db $DB_PATH
Restart=on-failure
RestartSec=5s

# The server only ever writes inside its own data directory.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=read-only
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$APP_NAME" >/dev/null 2>&1
info "unit written: $UNIT_PATH"

# The first start creates the database and runs the migrations; the settings
# below are written into that database while the server is stopped.
step "Creating the database"

# Every sqlite3 call goes through the service user. Run as root, the CLI creates
# a root-owned database file the server can then never write to — and because
# sqlite3 creates the file merely by opening it, polling with it would lose the
# race against the starting server and break the install (this really happened).
as_svc() {
  if command -v runuser >/dev/null 2>&1; then runuser -u "$SVC_USER" -- "$@"
  else su -s /bin/sh "$SVC_USER" -c "$(printf '%q ' "$@")"; fi
}

# A leftover empty file from an earlier attempt is exactly that failure state.
if [ -f "$DB_PATH" ] && [ ! -s "$DB_PATH" ]; then
  rm -f "$DB_PATH" "$DB_PATH-wal" "$DB_PATH-shm"
  note "removed an empty database file left by an earlier attempt"
fi

systemctl restart "$APP_NAME"

# Wait for the server itself to create the file, then check the migrations ran.
db_ready() {
  [ -s "$DB_PATH" ] || return 1
  [ "$SKIP_SEED" = 1 ] && return 0   # an old CLI cannot read STRICT tables
  as_svc sqlite3 "$DB_PATH" 'SELECT count(*) FROM settings;' >/dev/null 2>&1
}
for _ in $(seq 1 30); do
  db_ready && break
  sleep 1
done
if ! db_ready; then
  printf '\n'
  journalctl -u "$APP_NAME" -n 30 --no-pager 2>/dev/null || systemctl status "$APP_NAME" --no-pager -l || true
  die "the server did not create the database (the log above says why)"
fi
info "database ready: $DB_PATH"
# From here until the final start the service is down. Whatever happens — an
# error, a Ctrl+C, a question the operator walks away from — it must not be left
# that way: the cleanup trap starts it again.
NEED_START=1
systemctl stop "$APP_NAME"

# --- 5. settings -------------------------------------------------------------

SQL_FILE="$(mktemp)"
LIST_FILE="$(mktemp)"

sq() { printf '%s' "$1" | sed "s/'/''/g"; }
set_setting() {
  # strftime rather than unixepoch(): the latter arrived in SQLite 3.38 and the
  # CLI on a stable distribution is often older than that. The CAST keeps the
  # value an integer, which the STRICT table requires.
  printf "INSERT INTO settings (key, value, updated_at) VALUES ('%s','%s',CAST(strftime('%%s','now') AS INTEGER))\n" \
    "$(sq "$1")" "$(sq "$2")" >> "$SQL_FILE"
  printf "  ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at;\n" \
    >> "$SQL_FILE"
  printf '  %s = %s\n' "$1" "$2" >> "$LIST_FILE"
}

step "Server settings"
note "everything here can be changed later with 'tamizchat-panel'"

SERVER_NAME="$OPT_NAME"
[ -n "$SERVER_NAME" ] || SERVER_NAME="$(ask 'Server name' 'TamizChat Server')"
set_setting server.name "$SERVER_NAME"

SERVER_PASSWORD="$OPT_PASSWORD"
if [ -z "$SERVER_PASSWORD" ] && [ "$ASSUME_YES" != 1 ]; then
  if confirm "Protect the server with a password?" Y; then
    SERVER_PASSWORD="$(ask_secret 'Server password')"
  fi
fi
if [ -n "$SERVER_PASSWORD" ]; then
  set_setting server.password "$SERVER_PASSWORD"
  info "password set"
else
  note "no password: anyone who knows the address can join"
fi

# nginx?
DOMAIN="$OPT_DOMAIN"
if [ -z "$DOMAIN" ] && [ "$ASSUME_YES" != 1 ]; then
  DOMAIN="$(ask 'Domain name (empty = connect by IP)' '')"
fi

if [ "$USE_NGINX" = -1 ]; then
  if [ -n "$DOMAIN" ]; then
    confirm "Put nginx in front of the server? (recommended, and needed for TLS)" Y && USE_NGINX=1 || USE_NGINX=0
  else
    USE_NGINX=0
  fi
fi

LISTEN="$OPT_LISTEN"
if [ -z "$LISTEN" ]; then
  if [ "$USE_NGINX" = 1 ]; then LISTEN="127.0.0.1:8080"; else LISTEN="$(ask 'Listen address' ':8080')"; fi
fi
set_setting network.listen_addr "$LISTEN"
LISTEN_PORT="${LISTEN##*:}"

if [ "$USE_NGINX" = 1 ]; then
  set_setting network.trusted_proxies "127.0.0.1"
fi

# --- 6. nginx and TLS --------------------------------------------------------

SCHEME="http"
if [ "$USE_NGINX" = 1 ]; then
  step "nginx"
  [ -n "$DOMAIN" ] || DOMAIN="$(ask 'Domain name for nginx' 'localhost')"
  need_cmd nginx nginx || die "could not install nginx"

  mkdir -p "$(dirname "$NGINX_SITE")" /etc/nginx/sites-enabled
  cat > "$NGINX_SITE" <<EOF
server {
    listen 80;
    server_name $DOMAIN;

    location / {
        proxy_pass http://$LISTEN;
        proxy_http_version 1.1;

        # Without these two the WebSocket never upgrades and nothing works.
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_set_header Host \$host;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # Sockets stay open for hours; the default would cut them at 60s.
        proxy_read_timeout 3600s;

        # Must exceed uploads.max_size_mb, or large files fail at nginx.
        client_max_body_size 64m;
    }
}
EOF
  ln -sf "$NGINX_SITE" "$NGINX_LINK"

  # Debian ships a default site on port 80 that would answer first.
  if [ -L /etc/nginx/sites-enabled/default ]; then
    rm -f /etc/nginx/sites-enabled/default
    note "the distribution's default nginx site was disabled"
  fi
  # On RHEL-like systems sites-enabled is not included by default.
  if [ -f /etc/nginx/nginx.conf ] && ! grep -q 'sites-enabled' /etc/nginx/nginx.conf; then
    warn "add this line inside the http { } block of /etc/nginx/nginx.conf:"
    note "  include /etc/nginx/sites-enabled/*;"
  fi

  nginx -t >/dev/null 2>&1 || { nginx -t; die "the nginx config is not valid"; }
  systemctl enable nginx >/dev/null 2>&1 || true
  systemctl restart nginx
  info "nginx serves $DOMAIN and proxies to $LISTEN"

  if [ "$USE_TLS" = -1 ]; then
    if [ -n "$DOMAIN" ] && [ "$DOMAIN" != "localhost" ]; then
      confirm "Get a free Let's Encrypt certificate now? (the domain must already point here)" Y && USE_TLS=1 || USE_TLS=0
    else
      USE_TLS=0
    fi
  fi

  if [ "$USE_TLS" = 1 ]; then
    step "TLS certificate"
    need_cmd certbot certbot python3-certbot-nginx || warn "could not install certbot"
    if command -v certbot >/dev/null 2>&1; then
      EMAIL="$OPT_EMAIL"
      [ -n "$EMAIL" ] || EMAIL="$(ask 'E-mail for Let'"'"'s Encrypt (renewal notices)' '')"
      if [ -n "$EMAIL" ]; then
        CERTBOT_ARGS=(--nginx -d "$DOMAIN" --non-interactive --agree-tos -m "$EMAIL" --redirect)
      else
        CERTBOT_ARGS=(--nginx -d "$DOMAIN" --non-interactive --agree-tos --register-unsafely-without-email --redirect)
      fi
      if certbot "${CERTBOT_ARGS[@]}"; then
        SCHEME="https"
        info "certificate installed; renewal is certbot's own timer"
      else
        warn "certbot failed - most often the domain does not resolve here yet, or port 80 is closed."
        warn "the server still works over http. Retry later with: sudo certbot --nginx -d $DOMAIN"
      fi
    fi
  fi
fi

# public_host is the address clients actually type, port included when there is
# no proxy in front to hide it.
if [ -n "$DOMAIN" ]; then
  if [ "$USE_NGINX" = 1 ]; then
    set_setting network.public_host "$SCHEME://$DOMAIN"
  else
    set_setting network.public_host "http://$DOMAIN:$LISTEN_PORT"
  fi
fi

# --- 7. LiveKit (voice and video) -------------------------------------------

guess_ip() {
  local ip
  ip="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')"
  [ -n "$ip" ] || ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  printf '%s' "$ip"
}

if [ "$USE_LIVEKIT" = -1 ]; then
  if [ "$ASSUME_YES" = 1 ]; then
    # Installing Docker is too large a side effect to take on silently; ask for
    # it explicitly with --livekit.
    USE_LIVEKIT=0
  else
    printf '\n'
    note "Voice, video and music bots need LiveKit, a separate service."
    note "Text chat, rooms, files and the paint board work without it."
    confirm "Install LiveKit with Docker now?" Y && USE_LIVEKIT=1 || USE_LIVEKIT=0
  fi
fi

if [ "$USE_LIVEKIT" = 1 ]; then
  step "LiveKit"

  if ! command -v docker >/dev/null 2>&1; then
    if confirm "Docker is not installed. Install it from get.docker.com?" Y; then
      curl -fsSL https://get.docker.com | sh || warn "the Docker install failed"
    fi
  fi

  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    LK_HOST="$DOMAIN"
    [ -n "$LK_HOST" ] || LK_HOST="$(ask 'Address clients reach this server on (IP or domain)' "$(guess_ip)")"

    # Reuse the keys of an existing installation. Generating new ones on a
    # re-run would leave the running container on the old pair — the config is
    # bind-mounted, so `compose up -d` alone does not reload it.
    LK_KEY=""; LK_SECRET=""
    if [ -f "$LIVEKIT_DIR/livekit.yaml" ]; then
      LK_KEY="$(awk '/^keys:/{getline; gsub(/[: ].*/,"",$1); print $1; exit}' "$LIVEKIT_DIR/livekit.yaml")"
      LK_SECRET="$(awk '/^keys:/{getline; sub(/^[^:]*:[ ]*/,""); print; exit}' "$LIVEKIT_DIR/livekit.yaml")"
      [ -n "$LK_KEY" ] && [ -n "$LK_SECRET" ] && info "reusing the existing LiveKit keys"
    fi
    if [ -z "$LK_KEY" ] || [ -z "$LK_SECRET" ]; then
      LK_KEY="API$(openssl rand -hex 6 2>/dev/null || head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
      LK_SECRET="$(openssl rand -base64 32 2>/dev/null | tr -d '\n' || head -c 32 /dev/urandom | base64 | tr -d '\n')"
    fi

    mkdir -p "$LIVEKIT_DIR"
    cat > "$LIVEKIT_DIR/livekit.yaml" <<EOF
# Generated by the TamizChat installer.
port: 7880
log_level: info

rtc:
  udp_port: 7882
  tcp_port: 7881
  # On a VPS with a public IP this works out the address by itself. Behind a
  # home router, set use_external_ip to false and write node_ip by hand, then
  # forward 7880/tcp, 7881/tcp and 7882/udp on the router.
  use_external_ip: true

keys:
  $LK_KEY: $LK_SECRET
EOF
    cat > "$LIVEKIT_DIR/docker-compose.yml" <<'EOF'
# Generated by the TamizChat installer.
#
# host networking is required: LiveKit has to announce its real address in ICE
# and take the UDP port directly. With bridge networking the media connection
# never establishes.
services:
  livekit:
    image: livekit/livekit-server:latest
    container_name: tamizchat-livekit
    restart: unless-stopped
    network_mode: host
    command: --config /etc/livekit.yaml
    volumes:
      - ./livekit.yaml:/etc/livekit.yaml:ro
EOF
    chmod 0600 "$LIVEKIT_DIR/livekit.yaml"

    # The config is a bind mount, so a plain `up -d` on an already-running
    # container would keep serving the old file; force it to re-read.
    (cd "$LIVEKIT_DIR" && docker compose up -d && docker compose restart livekit >/dev/null) \
      || warn "LiveKit did not start; check: docker compose -f $LIVEKIT_DIR/docker-compose.yml logs"

    set_setting livekit.url "ws://$LK_HOST:7880"
    set_setting livekit.api_key "$LK_KEY"
    set_setting livekit.api_secret "$LK_SECRET"
    set_setting livekit.enabled true
    # This server reaches LiveKit locally even when clients use a public name.
    set_setting livekit.api_url "http://127.0.0.1:7880"

    info "LiveKit is up on 7880/tcp, 7881/tcp and 7882/udp"
    info "clients will use ws://$LK_HOST:7880"
    note "config and keys: $LIVEKIT_DIR/livekit.yaml"
  else
    USE_LIVEKIT=0
    warn "Docker is not available; skipping LiveKit. Voice and video stay off."
    note "you can add it later: see docs/LIVEKIT.md, then panel option 2 -> 9"
  fi
fi

# --- 8. write the settings ---------------------------------------------------

step "Writing the settings"
if [ "$SKIP_SEED" = 1 ]; then
  warn "sqlite3 $SQLITE_VER is too old to write into this database (3.37+ is needed)."
  warn "Nothing is lost - enter these in 'tamizchat-panel', option 2:"
  printf '\n'
  cat "$LIST_FILE"
  printf '\n'
  note "the server keeps its defaults until you do: listen :8080, no password"
  # The listen address chosen above was not applied, so the rest of the script
  # must check the default port instead.
  LISTEN=":8080"; LISTEN_PORT="8080"
else
  # The file stays at mktemp's 0600 (it holds the server password and the LiveKit
  # secret); the redirection opens it as root and the child inherits the fd.
  as_svc sqlite3 "$DB_PATH" < "$SQL_FILE" || die "could not write the settings into $DB_PATH"
  info "$(grep -c '^INSERT' "$SQL_FILE") settings stored in the database"
fi
chown -R "$SVC_USER:$SVC_USER" "$APP_DIR"

# A port below 1024 cannot be bound by a non-root user; give the binary the
# capability instead of running the server as root.
case "$LISTEN_PORT" in
  ''|*[!0-9]*) : ;;
  *) if [ "$LISTEN_PORT" -lt 1024 ]; then
       if need_cmd setcap libcap2-bin libcap; then
         setcap 'cap_net_bind_service=+ep' "$BIN_PATH" && info "granted the binary permission to bind port $LISTEN_PORT"
       else
         warn "port $LISTEN_PORT is privileged and setcap is missing; the service may fail to start"
       fi
     fi ;;
esac

# --- 9. firewall -------------------------------------------------------------

if [ "$USE_FIREWALL" = -1 ]; then
  if command -v ufw >/dev/null 2>&1 || command -v firewall-cmd >/dev/null 2>&1; then
    confirm "Open the needed ports in the firewall?" Y && USE_FIREWALL=1 || USE_FIREWALL=0
  else
    USE_FIREWALL=0
  fi
fi

if [ "$USE_FIREWALL" = 1 ]; then
  step "Firewall"
  open_tcp() { # port
    if command -v ufw >/dev/null 2>&1; then ufw allow "$1/tcp" >/dev/null 2>&1 && info "opened $1/tcp"
    elif command -v firewall-cmd >/dev/null 2>&1; then firewall-cmd --permanent --add-port="$1/tcp" >/dev/null 2>&1 && info "opened $1/tcp"; fi
  }
  open_udp() {
    if command -v ufw >/dev/null 2>&1; then ufw allow "$1/udp" >/dev/null 2>&1 && info "opened $1/udp"
    elif command -v firewall-cmd >/dev/null 2>&1; then firewall-cmd --permanent --add-port="$1/udp" >/dev/null 2>&1 && info "opened $1/udp"; fi
  }

  if [ "$USE_NGINX" = 1 ]; then
    open_tcp 80
    [ "$SCHEME" = "https" ] && open_tcp 443
  else
    case "$LISTEN_PORT" in ''|*[!0-9]*) : ;; *) open_tcp "$LISTEN_PORT" ;; esac
  fi
  if [ "$USE_LIVEKIT" = 1 ]; then
    open_tcp 7880; open_tcp 7881; open_udp 7882
  fi
  command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --reload >/dev/null 2>&1 || true
fi

# --- 10. nightly backup ------------------------------------------------------

if [ "$USE_BACKUP_CRON" = -1 ]; then
  confirm "Add a nightly database backup at 04:00?" Y && USE_BACKUP_CRON=1 || USE_BACKUP_CRON=0
fi

if [ "$USE_BACKUP_CRON" = 1 ]; then
  step "Nightly backup"
  mkdir -p "$DATA_DIR/backups"
  chown "$SVC_USER:$SVC_USER" "$DATA_DIR/backups"
  # VACUUM INTO is safe while the server is running, unlike copying the file.
  cat > /etc/cron.d/tamizchat-backup <<EOF
# TamizChat nightly backup. VACUUM INTO is safe on a running database.
0 4 * * * $SVC_USER sqlite3 $DB_PATH "VACUUM INTO '$DATA_DIR/backups/nightly-\$(date +\\%Y\\%m\\%d).db'" && find $DATA_DIR/backups -name 'nightly-*.db' -mtime +14 -delete
EOF
  chmod 0644 /etc/cron.d/tamizchat-backup
  info "/etc/cron.d/tamizchat-backup (keeps 14 days)"
fi

# --- 11. start and verify ----------------------------------------------------

step "Starting the server"
systemctl restart "$APP_NAME"
NEED_START=0
sleep 2

HEALTH_URL="http://${LISTEN/#:/127.0.0.1:}/healthz"
OK=0
for _ in $(seq 1 15); do
  if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then OK=1; break; fi
  sleep 1
done

if [ "$OK" = 1 ]; then
  info "${GREEN}the server answers on $HEALTH_URL${R}"
else
  warn "no answer from $HEALTH_URL yet"
  systemctl status "$APP_NAME" --no-pager -l | head -20 || true
  note "logs: journalctl -u $APP_NAME -n 50"
fi

# --- 12. what to do next -----------------------------------------------------

CONNECT="$(guess_ip):${LISTEN_PORT}"
[ "$USE_NGINX" = 1 ] && [ -n "$DOMAIN" ] && CONNECT="$DOMAIN"

cat <<EOF

${GREEN}${B}Installed.${R}

  ${B}Clients connect to:${R}  $CONNECT
  ${B}Admin panel:${R}         tamizchat-panel
  ${B}Service:${R}             systemctl {status|restart|stop} $APP_NAME
  ${B}Logs:${R}                journalctl -u $APP_NAME -f
  ${B}Data:${R}                $DATA_DIR

${B}One thing is still missing: an administrator.${R}
Nobody can create a room or moderate until somebody holds the Admin role, and
that first grant has to happen here on the machine, by design:

  1. Connect once with the client, so the server learns your client ID.
  2. Run  ${B}tamizchat-panel${R}
  3. Option ${B}7${R} -> ${B}5${R} (Grant a role to a user) -> Admin -> yourself
  4. Option ${B}6${R} to create rooms

Everything else - server name, limits, uploads, LiveKit - is option ${B}2${R} in the
same panel, and applies without a restart (except the listen address).

EOF

if [ "$USE_LIVEKIT" = 1 ]; then
  cat <<EOF
${B}Voice and video${R} are configured and enabled. Check panel option ${B}11${R}: it should
say "Voice/video: enabled". If there is no sound, the UDP port 7882 is the
first thing to check.

EOF
fi

if [ "$USE_NGINX" = 1 ] && [ "$SCHEME" = "http" ]; then
  cat <<EOF
${YELLOW}Without TLS everything travels in the clear${R} - messages, the server password
and files. When the domain points here, run:

  sudo certbot --nginx -d $DOMAIN

then set ${B}network.public_host${R} to https://$DOMAIN in the panel.

EOF
fi
