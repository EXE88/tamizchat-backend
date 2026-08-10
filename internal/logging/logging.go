// Package logging builds the process-wide structured logger.
package logging

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

var (
	mu    sync.Mutex
	level = new(slog.LevelVar)
)

// Setup installs a text handler on stderr and returns the root logger.
// It is safe to call once at startup; use SetLevel afterwards to change verbosity.
func Setup(lvl string) *slog.Logger {
	level.Set(ParseLevel(lvl))
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}

// SetLevel changes the verbosity of the already installed handler at runtime,
// so the admin panel can turn on debug logs without a restart.
func SetLevel(lvl string) {
	mu.Lock()
	defer mu.Unlock()
	level.Set(ParseLevel(lvl))
}

// ParseLevel maps a config string to a slog level, falling back to info.
func ParseLevel(lvl string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
