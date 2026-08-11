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
	Title    string   // Persian label shown in the panel
	Help     string   // Persian one-line explanation
	Validate func(string) error
}

// Sections in the order the admin panel should show them.
var Sections = []string{"server", "network", "users", "rooms", "chat", "uploads", "livekit", "log"}

var registry = buildRegistry(
	Setting{
		Key: KeyServerName, Section: "server", Kind: KindString, Default: "TamizChat Server",
		Title: "نام سرور", Help: "نامی که کلاینت‌ها هنگام اتصال می‌بینند.",
		Validate: notEmpty,
	},
	Setting{
		Key: KeyServerWelcome, Section: "server", Kind: KindString, Default: "به سرور خوش آمدید!",
		Title: "پیام خوش‌آمد", Help: "پس از اتصال موفق برای کاربر ارسال می‌شود.",
	},
	Setting{
		Key: KeyServerPassword, Section: "server", Kind: KindSecret, Default: "",
		Title: "رمز ورود سرور", Help: "خالی یعنی سرور باز است و هرکسی می‌تواند وصل شود.",
	},
	Setting{
		Key: KeyServerMaxUsers, Section: "server", Kind: KindInt, Default: "200",
		Title: "حداکثر کاربر همزمان", Help: "سقف تعداد نشست‌های فعال روی کل سرور.",
		Validate: intRange(1, 100000),
	},

	Setting{
		Key: KeyListenAddr, Section: "network", Kind: KindString, Default: ":8080",
		Title: "آدرس گوش‌دادن", Help: "مثلاً :8080 یا 127.0.0.1:8080 — پس از تغییر نیاز به ری‌استارت دارد.",
		Validate: validListenAddr,
	},
	Setting{
		Key: KeyPublicHost, Section: "network", Kind: KindString, Default: "",
		Title: "هاست عمومی", Help: "دامنه یا آی‌پی که کلاینت‌ها با آن وصل می‌شوند؛ برای ساخت لینک‌ها استفاده می‌شود.",
	},

	Setting{
		Key: KeyHeartbeatSec, Section: "network", Kind: KindInt, Default: "30",
		Title: "فاصلهٔ ضربان (ثانیه)", Help: "هر چند ثانیه سرور کلاینت را ping کند تا اتصال‌های مرده تشخیص داده شوند.",
		Validate: intRange(5, 600),
	},

	Setting{
		Key: KeyUsernameMinLen, Section: "users", Kind: KindInt, Default: "3",
		Title: "حداقل طول نام کاربری", Help: "تعداد کاراکتر.",
		Validate: intRange(1, 64),
	},
	Setting{
		Key: KeyUsernameMaxLen, Section: "users", Kind: KindInt, Default: "24",
		Title: "حداکثر طول نام کاربری", Help: "تعداد کاراکتر؛ باید از حداقل بزرگ‌تر باشد.",
		Validate: intRange(1, 64),
	},

	Setting{
		Key: KeyRoomsMaxPerServer, Section: "rooms", Kind: KindInt, Default: "50",
		Title: "حداکثر تعداد روم", Help: "سقف روم‌هایی که روی این سرور ساخته می‌شود.",
		Validate: intRange(1, 10000),
	},
	Setting{
		Key: KeyRoomsDefaultMaxUsers, Section: "rooms", Kind: KindInt, Default: "25",
		Title: "ظرفیت پیش‌فرض روم", Help: "مقدار اولیهٔ ظرفیت هنگام ساخت روم جدید.",
		Validate: intRange(1, 5000),
	},
	Setting{
		Key: KeyRoomsHistoryLimit, Section: "rooms", Kind: KindInt, Default: "500",
		Title: "سقف پیام در حافظهٔ روم", Help: "پیام‌های قدیمی‌تر از این تعداد از حافظهٔ موقت روم حذف می‌شوند.",
		Validate: intRange(10, 100000),
	},
	Setting{
		Key: KeyRoomsPurgeOnEmpty, Section: "rooms", Kind: KindBool, Default: "true",
		Title: "پاک‌سازی روم پس از خالی‌شدن", Help: "با خروج آخرین نفر، چت و فایل‌های روم پاک می‌شوند.",
	},
	Setting{
		Key: KeyRoomsPurgeGraceSec, Section: "rooms", Kind: KindInt, Default: "30",
		Title: "مهلت پاک‌سازی (ثانیه)", Help: "اگر کسی در این بازه برگردد، محتوای روم حفظ می‌شود.",
		Validate: intRange(0, 86400),
	},

	Setting{
		Key: KeyChatMaxMessageLen, Section: "chat", Kind: KindInt, Default: "2000",
		Title: "حداکثر طول پیام", Help: "تعداد کاراکتر هر پیام متنی.",
		Validate: intRange(1, 20000),
	},
	Setting{
		Key: KeyChatRatePerMinute, Section: "chat", Kind: KindInt, Default: "30",
		Title: "سقف پیام در دقیقه", Help: "نرخ پایدار ارسال پیام برای هر کاربر.",
		Validate: intRange(1, 600),
	},
	Setting{
		Key: KeyChatRateBurst, Section: "chat", Kind: KindInt, Default: "5",
		Title: "پیام پشت‌سرهم مجاز", Help: "چند پیام بی‌وقفه بعد از یک مکث پذیرفته می‌شود.",
		Validate: intRange(1, 100),
	},
	Setting{
		Key: KeyChatStickersEnabled, Section: "chat", Kind: KindBool, Default: "true",
		Title: "فعال بودن استیکر", Help: "اجازهٔ ارسال استیکر در روم‌ها.",
	},

	Setting{
		Key: KeyUploadsEnabled, Section: "uploads", Kind: KindBool, Default: "true",
		Title: "فعال بودن ارسال فایل", Help: "اجازهٔ ارسال فایل و عکس در روم‌ها.",
	},
	Setting{
		Key: KeyUploadsMaxSizeMB, Section: "uploads", Kind: KindInt, Default: "25",
		Title: "حداکثر حجم هر فایل (مگابایت)", Help: "فایل‌های بزرگ‌تر رد می‌شوند.",
		Validate: intRange(1, 2048),
	},
	Setting{
		Key: KeyUploadsRoomQuotaMB, Section: "uploads", Kind: KindInt, Default: "512",
		Title: "سهمیهٔ فایل هر روم (مگابایت)", Help: "مجموع حجم فایل‌های زندهٔ یک روم.",
		Validate: intRange(1, 102400),
	},
	Setting{
		Key: KeyUploadsDir, Section: "uploads", Kind: KindString, Default: "data/uploads",
		Title: "مسیر فایل‌های موقت", Help: "محل نگهداری موقت فایل‌ها تا زمان پاک‌سازی روم.",
		Validate: notEmpty,
	},

	Setting{
		Key: KeyLiveKitEnabled, Section: "livekit", Kind: KindBool, Default: "false",
		Title: "فعال بودن ویس/ویدیو", Help: "تا وقتی LiveKit تنظیم نشده، خاموش بماند.",
	},
	Setting{
		Key: KeyLiveKitURL, Section: "livekit", Kind: KindString, Default: "",
		Title: "آدرس LiveKit", Help: "مثلاً ws://127.0.0.1:7880 — کلاینت با این آدرس به مدیا وصل می‌شود.",
	},
	Setting{
		Key: KeyLiveKitAPIKey, Section: "livekit", Kind: KindString, Default: "",
		Title: "LiveKit API Key", Help: "کلید صادرشده در کانفیگ LiveKit.",
	},
	Setting{
		Key: KeyLiveKitAPISecret, Section: "livekit", Kind: KindSecret, Default: "",
		Title: "LiveKit API Secret", Help: "با این مقدار توکن اتصال کاربران امضا می‌شود.",
	},

	Setting{
		Key: KeyLogLevel, Section: "log", Kind: KindEnum, Default: "info",
		Options: []string{"debug", "info", "warn", "error"},
		Title:   "سطح لاگ", Help: "بدون ری‌استارت اعمال می‌شود.",
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
			return fmt.Errorf("%s: مقدار باید عدد باشد", s.Key)
		}
	case KindBool:
		if _, err := parseBool(value); err != nil {
			return fmt.Errorf("%s: مقدار باید true یا false باشد", s.Key)
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
			return fmt.Errorf("%s: مقدار مجاز یکی از %s است", s.Key, strings.Join(s.Options, ", "))
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
		return fmt.Errorf("مقدار نمی‌تواند خالی باشد")
	}
	return nil
}

func intRange(min, max int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("مقدار باید عدد باشد")
		}
		if n < min || n > max {
			return fmt.Errorf("مقدار باید بین %d و %d باشد", min, max)
		}
		return nil
	}
}

func validListenAddr(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("آدرس نمی‌تواند خالی باشد")
	}
	if _, _, err := net.SplitHostPort(v); err != nil {
		return fmt.Errorf("قالب درست مثل :8080 یا 127.0.0.1:8080 است")
	}
	return nil
}
