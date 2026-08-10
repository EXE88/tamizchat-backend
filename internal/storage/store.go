// Package storage owns the on-disk SQLite database that holds everything the
// server must remember across restarts: settings, admins, roles, bans, bots.
// Room content (messages, files, drawings) is deliberately NOT stored here —
// it lives in memory and dies with the room.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // CGo-free SQLite driver
)

// Store is a thin wrapper around *sql.DB with the pragmas TamizChat needs.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (and creates if missing) the database at path and applies all
// pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}

	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc's driver is safe for concurrent use, but a single writer avoids
	// SQLITE_BUSY churn under the write-light load this server produces.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &Store{db: db, path: path}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying handle for packages that own their own tables.
func (s *Store) DB() *sql.DB { return s.db }

// Path is the database file location, shown in the admin panel.
func (s *Store) Path() string { return s.path }

// Close flushes and closes the database.
func (s *Store) Close() error { return s.db.Close() }
