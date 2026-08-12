package config

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// SettingsStore is the persistence contract config needs; storage.Store fits it.
type SettingsStore interface {
	LoadSettings(ctx context.Context) (map[string]string, error)
	PutSetting(ctx context.Context, key, value string) error
	DeleteSetting(ctx context.Context, key string) error
}

// Config is a live, concurrency-safe view of the server settings. Readers get
// the current value; the admin panel writes through it and persists.
type Config struct {
	store SettingsStore

	mu       sync.RWMutex
	values   map[string]string
	watchers []func(key, value string)
}

// Load reads persisted settings and layers them over the registry defaults.
func Load(ctx context.Context, store SettingsStore) (*Config, error) {
	saved, err := store.LoadSettings(ctx)
	if err != nil {
		return nil, err
	}

	values := make(map[string]string, len(registry))
	for _, s := range registry {
		values[s.Key] = s.Default
	}
	for k, v := range saved {
		s, ok := Lookup(k)
		if !ok {
			// A key from an older build: keep it out of the live view rather
			// than letting an unknown value influence behaviour.
			continue
		}
		if err := s.check(v); err != nil {
			return nil, fmt.Errorf("invalid stored value: %w", err)
		}
		values[k] = v
	}

	return &Config{store: store, values: values}, nil
}

// Reload re-reads every setting from the database and publishes what changed.
// It is how an edit made by the admin panel — which writes straight to the
// database — reaches a server that is already running.
//
// It returns the number of values that actually changed, so the panel can say
// what happened rather than just "done".
func (c *Config) Reload(ctx context.Context) (int, error) {
	saved, err := c.store.LoadSettings(ctx)
	if err != nil {
		return 0, err
	}

	fresh := make(map[string]string, len(registry))
	for _, s := range registry {
		fresh[s.Key] = s.Default
	}
	for k, v := range saved {
		s, ok := Lookup(k)
		if !ok {
			continue
		}
		if err := s.check(v); err != nil {
			// One bad row must not take the whole configuration down: keep the
			// value that is already running and report it.
			return 0, fmt.Errorf("invalid stored value: %w", err)
		}
		fresh[k] = v
	}

	c.mu.Lock()
	type change struct{ key, value string }
	var changed []change
	for key, value := range fresh {
		if c.values[key] != value {
			changed = append(changed, change{key, value})
		}
	}
	c.values = fresh
	watchers := append([]func(string, string){}, c.watchers...)
	c.mu.Unlock()

	for _, ch := range changed {
		for _, w := range watchers {
			w(ch.key, ch.value)
		}
	}
	return len(changed), nil
}

// Set validates, persists, and publishes a new value for key.
func (c *Config) Set(ctx context.Context, key, value string) error {
	s, ok := Lookup(key)
	if !ok {
		return fmt.Errorf("unknown key: %s", key)
	}
	value = strings.TrimSpace(value)
	if err := s.check(value); err != nil {
		return err
	}
	if err := c.store.PutSetting(ctx, key, value); err != nil {
		return err
	}

	c.mu.Lock()
	c.values[key] = value
	watchers := append([]func(string, string){}, c.watchers...)
	c.mu.Unlock()

	for _, w := range watchers {
		w(key, value)
	}
	return nil
}

// Reset restores a key to its built-in default.
func (c *Config) Reset(ctx context.Context, key string) error {
	s, ok := Lookup(key)
	if !ok {
		return fmt.Errorf("unknown key: %s", key)
	}
	if err := c.store.DeleteSetting(ctx, key); err != nil {
		return err
	}
	c.mu.Lock()
	c.values[key] = s.Default
	watchers := append([]func(string, string){}, c.watchers...)
	c.mu.Unlock()

	for _, w := range watchers {
		w(key, s.Default)
	}
	return nil
}

// Watch registers a callback fired after any successful Set or Reset. It is how
// components like the logger pick up changes without a restart.
func (c *Config) Watch(fn func(key, value string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watchers = append(c.watchers, fn)
}

// String returns the current value of key ("" if the key is unknown).
func (c *Config) String(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.values[key]
}

// Int returns key as an int, falling back to the registry default on any
// parse failure — values are validated on write, so this should not happen.
func (c *Config) Int(key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.String(key)))
	if err != nil {
		if s, ok := Lookup(key); ok {
			d, _ := strconv.Atoi(s.Default)
			return d
		}
		return 0
	}
	return n
}

// Bool returns key as a bool.
func (c *Config) Bool(key string) bool {
	b, err := parseBool(c.String(key))
	if err != nil {
		return false
	}
	return b
}

// UsernameLimits returns the name length bounds, guaranteeing min <= max even
// if the two settings were edited into an inconsistent state.
func (c *Config) UsernameLimits() (min, max int) {
	min, max = c.Int(KeyUsernameMinLen), c.Int(KeyUsernameMaxLen)
	if min > max {
		min, max = max, min
	}
	return min, max
}

// Snapshot returns all current values, for the admin panel's list view.
func (c *Config) Snapshot() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

// Display renders a value for the panel, masking secrets.
func Display(s Setting, value string) string {
	if s.Kind == KindSecret {
		if value == "" {
			return "(not set)"
		}
		return "••••••••"
	}
	if value == "" {
		return "(empty)"
	}
	return value
}
