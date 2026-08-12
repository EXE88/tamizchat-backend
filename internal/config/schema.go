// Package config holds every tunable of the server. Values live in SQLite
// rather than a .env file so the admin panel can read and edit them at runtime.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Kind tells the admin panel how to render and validate a setting.
type Kind string

const (
	KindString Kind = "string"
	KindInt    Kind = "int"
	KindBool   Kind = "bool"
	KindSecret Kind = "secret" // masked in listings
	KindEnum   Kind = "enum"
)

// Setting is the definition of one configurable key. The registry below is the
// single source of truth: the panel enumerates it, and nothing may be set that
// is not declared here.
type Setting struct {
	Key      string
	Section  string
	Kind     Kind
	Default  string
	Options  []string // for KindEnum
	Title    string   // label shown in the panel
	Help     string   // one-line explanation
	Validate func(string) error
}

// Sections in the order the admin panel should show them.
var Sections = []string{"server", "network", "tls", "users", "rooms", "chat",
	"uploads", "paint", "livekit", "backup", "log"}

var registry = buildRegistry(
	Setting{
		Key: KeyServerName, Section: "server", Kind: KindString, Default: "TamizChat Server",
		Title: "Server name", Help: "The name clients see when they connect.",
		Validate: notEmpty,
	},
	Setting{
		Key: KeyServerWelcome, Section: "server", Kind: KindString, Default: "Welcome to the server!",
		Title: "Welcome message", Help: "Sent to the user after a successful connection.",
	},
	Setting{
		Key: KeyServerPassword, Section: "server", Kind: KindSecret, Default: "",
		Title: "Server password", Help: "Empty means the server is open and anyone may connect.",
	},
	Setting{
		Key: KeyServerMaxUsers, Section: "server", Kind: KindInt, Default: "200",
		Title: "Max concurrent users", Help: "Cap on active sessions across the whole server.",
		Validate: intRange(1, 100000),
	},

	Setting{
		Key: KeyListenAddr, Section: "network", Kind: KindString, Default: ":8080",
		Title: "Listen address", Help: "e.g. :8080 or 127.0.0.1:8080 — needs a restart to take effect.",
		Validate: validListenAddr,
	},
	Setting{
		Key: KeyPublicHost, Section: "network", Kind: KindString, Default: "",
		Title: "Public host", Help: "The domain or IP clients connect to; used to build links.",
	},

	Setting{
		Key: KeyHeartbeatSec, Section: "network", Kind: KindInt, Default: "30",
		Title: "Heartbeat interval (seconds)", Help: "How often the server pings a client so dead connections are noticed.",
		Validate: intRange(5, 600),
	},

	Setting{
		Key: KeyMaxConnsPerIP, Section: "network", Kind: KindInt, Default: "8",
		Title: "Max connections per IP", Help: "Zero means unlimited — for when everyone is behind one NAT.",
		Validate: intRange(0, 10000),
	},
	Setting{
		Key: KeyHandshakePerMinute, Section: "network", Kind: KindInt, Default: "60",
		Title: "New connections per minute", Help: "Per IP; stops connection floods.",
		Validate: intRange(1, 10000),
	},
	Setting{
		Key: KeyHandshakeBurst, Section: "network", Kind: KindInt, Default: "15",
		Title: "Connection burst allowance", Help: "A client hopping quickly between rooms must not get blocked.",
		Validate: intRange(1, 1000),
	},
	Setting{
		Key: KeyTrustedProxies, Section: "network", Kind: KindString, Default: "",
		Title:    "Trusted proxies",
		Help:     "Comma-separated IPs or CIDRs. X-Forwarded-For is believed only from these addresses.",
		Validate: validTrustedProxies,
	},

	Setting{
		Key: KeyTLSEnabled, Section: "tls", Kind: KindBool, Default: "false",
		Title: "TLS enabled", Help: "Behind nginx or Caddy, leave this off and let them terminate TLS.",
	},
	Setting{
		Key: KeyTLSCertFile, Section: "tls", Kind: KindString, Default: "",
		Title: "Certificate file (fullchain)", Help: "Path to the PEM file holding the certificate and its chain.",
	},
	Setting{
		Key: KeyTLSKeyFile, Section: "tls", Kind: KindString, Default: "",
		Title: "Private key file", Help: "Path to the private key PEM file.",
	},

	Setting{
		Key: KeyBackupDir, Section: "backup", Kind: KindString, Default: "data/backups",
		Title: "Backup folder", Help: "Database backups taken from the panel are written here.",
		Validate: notEmpty,
	},
	Setting{
		Key: KeyBackupKeep, Section: "backup", Kind: KindInt, Default: "10",
		Title: "Backups to keep", Help: "Older ones are removed after each new backup.",
		Validate: intRange(1, 1000),
	},

	Setting{
		Key: KeyUsernameMinLen, Section: "users", Kind: KindInt, Default: "3",
		Title: "Minimum username length", Help: "In characters.",
		Validate: intRange(1, 64),
	},
	Setting{
		Key: KeyUsernameMaxLen, Section: "users", Kind: KindInt, Default: "24",
		Title: "Maximum username length", Help: "In characters; must be greater than the minimum.",
		Validate: intRange(1, 64),
	},

	Setting{
		Key: KeyRoomsMaxPerServer, Section: "rooms", Kind: KindInt, Default: "50",
		Title: "Maximum number of rooms", Help: "Cap on rooms that can exist on this server.",
		Validate: intRange(1, 10000),
	},
	Setting{
		Key: KeyRoomsDefaultMaxUsers, Section: "rooms", Kind: KindInt, Default: "25",
		Title: "Default room capacity", Help: "Starting capacity when a new room is created.",
		Validate: intRange(1, 5000),
	},
	Setting{
		Key: KeyRoomsHistoryLimit, Section: "rooms", Kind: KindInt, Default: "500",
		Title: "Messages kept in room memory", Help: "Messages beyond this count are dropped from the room's temporary buffer.",
		Validate: intRange(10, 100000),
	},
	Setting{
		Key: KeyRoomsPurgeOnEmpty, Section: "rooms", Kind: KindBool, Default: "true",
		Title: "Purge a room once it empties", Help: "When the last member leaves, the room's chat and files are erased.",
	},
	Setting{
		Key: KeyRoomsPurgeGraceSec, Section: "rooms", Kind: KindInt, Default: "30",
		Title: "Purge grace period (seconds)", Help: "If somebody returns within this window, the room's content survives.",
		Validate: intRange(0, 86400),
	},

	Setting{
		Key: KeyChatMaxMessageLen, Section: "chat", Kind: KindInt, Default: "2000",
		Title: "Maximum message length", Help: "Characters per text message.",
		Validate: intRange(1, 20000),
	},
	Setting{
		Key: KeyChatRatePerMinute, Section: "chat", Kind: KindInt, Default: "30",
		Title: "Messages per minute", Help: "Sustained send rate per user.",
		Validate: intRange(1, 600),
	},
	Setting{
		Key: KeyChatRateBurst, Section: "chat", Kind: KindInt, Default: "5",
		Title: "Message burst allowance", Help: "How many back-to-back messages are accepted after a pause.",
		Validate: intRange(1, 100),
	},
	Setting{
		Key: KeyChatStickersEnabled, Section: "chat", Kind: KindBool, Default: "true",
		Title: "Stickers enabled", Help: "Allow stickers to be sent in rooms.",
	},

	Setting{
		Key: KeyUploadsEnabled, Section: "uploads", Kind: KindBool, Default: "true",
		Title: "File uploads enabled", Help: "Allow files and images to be sent in rooms.",
	},
	Setting{
		Key: KeyUploadsMaxSizeMB, Section: "uploads", Kind: KindInt, Default: "25",
		Title: "Maximum file size (MB)", Help: "Larger files are rejected.",
		Validate: intRange(1, 2048),
	},
	Setting{
		Key: KeyUploadsRoomQuotaMB, Section: "uploads", Kind: KindInt, Default: "512",
		Title: "Per-room file quota (MB)", Help: "Total size of a room's live files.",
		Validate: intRange(1, 102400),
	},
	Setting{
		Key: KeyUploadsDir, Section: "uploads", Kind: KindString, Default: "data/uploads",
		Title: "Temporary file folder", Help: "Where files are held until the room is purged.",
		Validate: notEmpty,
	},
	Setting{
		Key: KeyUploadsTokenTTLSec, Section: "uploads", Kind: KindInt, Default: "300",
		Title: "Download link lifetime (seconds)", Help: "A download link expires after this long.",
		Validate: intRange(10, 86400),
	},
	Setting{
		Key: KeyUploadsThumbMaxPx, Section: "uploads", Kind: KindInt, Default: "320",
		Title: "Thumbnail size", Help: "Longest side of the scaled-down image, in pixels.",
		Validate: intRange(32, 2048),
	},

	Setting{
		Key: KeyPaintEnabled, Section: "paint", Kind: KindBool, Default: "true",
		Title: "Paint board enabled", Help: "Allow shared drawing in rooms.",
	},
	Setting{
		Key: KeyPaintMaxStrokes, Section: "paint", Kind: KindInt, Default: "2000",
		Title: "Maximum strokes per board", Help: "Once full, the board must be cleared before drawing continues.",
		Validate: intRange(10, 100000),
	},
	Setting{
		Key: KeyPaintRatePerSecond, Section: "paint", Kind: KindInt, Default: "40",
		Title: "Paint messages per second", Help: "Sustained rate of drawing points per user.",
		Validate: intRange(1, 500),
	},
	Setting{
		Key: KeyPaintRateBurst, Section: "paint", Kind: KindInt, Default: "80",
		Title: "Paint burst allowance", Help: "So the start of a fast stroke does not come out choppy.",
		Validate: intRange(1, 1000),
	},

	Setting{
		Key: KeyLiveKitEnabled, Section: "livekit", Kind: KindBool, Default: "false",
		Title: "Voice/video enabled", Help: "Leave off until LiveKit is configured.",
	},
	Setting{
		Key: KeyLiveKitURL, Section: "livekit", Kind: KindString, Default: "",
		Title: "LiveKit URL", Help: "e.g. ws://127.0.0.1:7880 — the address clients use for media.",
	},
	Setting{
		Key: KeyLiveKitAPIKey, Section: "livekit", Kind: KindString, Default: "",
		Title: "LiveKit API Key", Help: "The key issued in the LiveKit config.",
	},
	Setting{
		Key: KeyLiveKitAPISecret, Section: "livekit", Kind: KindSecret, Default: "",
		Title: "LiveKit API Secret", Help: "Used to sign the tokens clients connect with.",
	},

	Setting{
		Key: KeyLogLevel, Section: "log", Kind: KindEnum, Default: "info",
		Options: []string{"debug", "info", "warn", "error"},
		Title:   "Log level", Help: "Applied without a restart.",
	},
)

// Setting keys. Use these constants instead of raw strings.
const (
	KeyServerName     = "server.name"
	KeyServerWelcome  = "server.welcome"
	KeyServerPassword = "server.password"
	KeyServerMaxUsers = "server.max_users"

	KeyListenAddr   = "network.listen_addr"
	KeyPublicHost   = "network.public_host"
	KeyHeartbeatSec = "network.heartbeat_sec"

	KeyMaxConnsPerIP      = "network.max_conns_per_ip"
	KeyHandshakePerMinute = "network.handshake_per_minute"
	KeyHandshakeBurst     = "network.handshake_burst"
	KeyTrustedProxies     = "network.trusted_proxies"

	KeyTLSEnabled  = "tls.enabled"
	KeyTLSCertFile = "tls.cert_file"
	KeyTLSKeyFile  = "tls.key_file"

	KeyBackupDir  = "backup.dir"
	KeyBackupKeep = "backup.keep"

	KeyUsernameMinLen = "users.username_min_len"
	KeyUsernameMaxLen = "users.username_max_len"

	KeyRoomsMaxPerServer    = "rooms.max_per_server"
	KeyRoomsDefaultMaxUsers = "rooms.default_max_users"
	KeyRoomsHistoryLimit    = "rooms.history_limit"
	KeyRoomsPurgeOnEmpty    = "rooms.purge_on_empty"
	KeyRoomsPurgeGraceSec   = "rooms.purge_grace_sec"

	KeyChatMaxMessageLen   = "chat.max_message_len"
	KeyChatRatePerMinute   = "chat.rate_per_minute"
	KeyChatRateBurst       = "chat.rate_burst"
	KeyChatStickersEnabled = "chat.stickers_enabled"

	KeyUploadsEnabled     = "uploads.enabled"
	KeyUploadsMaxSizeMB   = "uploads.max_size_mb"
	KeyUploadsRoomQuotaMB = "uploads.room_quota_mb"
	KeyUploadsDir         = "uploads.dir"
	KeyUploadsTokenTTLSec = "uploads.token_ttl_sec"
	KeyUploadsThumbMaxPx  = "uploads.thumb_max_px"

	KeyPaintEnabled       = "paint.enabled"
	KeyPaintMaxStrokes    = "paint.max_strokes"
	KeyPaintRatePerSecond = "paint.rate_per_second"
	KeyPaintRateBurst     = "paint.rate_burst"

	KeyLiveKitEnabled   = "livekit.enabled"
	KeyLiveKitURL       = "livekit.url"
	KeyLiveKitAPIKey    = "livekit.api_key"
	KeyLiveKitAPISecret = "livekit.api_secret"

	KeyLogLevel = "log.level"
)

func buildRegistry(settings ...Setting) []Setting { return settings }

// All returns every declared setting, in registry order.
func All() []Setting { return registry }

// Lookup finds a setting definition by key.
func Lookup(key string) (Setting, bool) {
	for _, s := range registry {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// BySection groups settings for panel rendering.
func BySection(section string) []Setting {
	var out []Setting
	for _, s := range registry {
		if s.Section == section {
			out = append(out, s)
		}
	}
	return out
}

// check validates a raw value against the setting's kind and custom validator.
func (s Setting) check(value string) error {
	switch s.Kind {
	case KindInt:
		if _, err := strconv.Atoi(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("%s: value must be a number", s.Key)
		}
	case KindBool:
		if _, err := parseBool(value); err != nil {
			return fmt.Errorf("%s: value must be true or false", s.Key)
		}
	case KindEnum:
		ok := false
		for _, o := range s.Options {
			if o == value {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s: value must be one of %s", s.Key, strings.Join(s.Options, ", "))
		}
	}
	if s.Validate != nil {
		return s.Validate(value)
	}
	return nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("invalid bool %q", v)
}

func notEmpty(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("value cannot be empty")
	}
	return nil
}

func intRange(min, max int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("value must be a number")
		}
		if n < min || n > max {
			return fmt.Errorf("value must be between %d and %d", min, max)
		}
		return nil
	}
}

// validTrustedProxies keeps a typo out of the security-relevant list: an
// unparseable entry would silently be ignored, quietly widening what the server
// believes.
func validTrustedProxies(v string) error {
	for _, entry := range strings.Split(v, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err == nil {
			continue
		}
		if net.ParseIP(entry) == nil {
			return fmt.Errorf("%q is neither an IP nor a CIDR", entry)
		}
	}
	return nil
}

func validListenAddr(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("address cannot be empty")
	}
	if _, _, err := net.SplitHostPort(v); err != nil {
		return fmt.Errorf("the format should look like :8080 or 127.0.0.1:8080")
	}
	return nil
}
