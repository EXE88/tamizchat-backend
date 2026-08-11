package storage

import (
	"context"
	"fmt"
	"log/slog"
)

// migration is one forward-only schema step. Never edit a shipped migration —
// append a new one instead, otherwise existing servers diverge.
type migration struct {
	name string
	stmt string
}

var migrations = []migration{
	{
		name: "0001_settings",
		stmt: `
CREATE TABLE settings (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at INTEGER NOT NULL
) STRICT;`,
	},
	{
		name: "0002_server_identity",
		stmt: `
CREATE TABLE server_identity (
	id          INTEGER PRIMARY KEY CHECK (id = 1),
	server_uuid TEXT NOT NULL,
	created_at  INTEGER NOT NULL
) STRICT;`,
	},
	{
		// Every client that has ever connected, keyed by the UUID the client
		// generated at install time. This is the identity the admin system
		// will hang roles and bans off in later phases.
		name: "0003_users",
		stmt: `
CREATE TABLE users (
	client_uuid   TEXT PRIMARY KEY,
	username      TEXT NOT NULL,
	first_seen_at INTEGER NOT NULL,
	last_seen_at  INTEGER NOT NULL,
	visit_count   INTEGER NOT NULL DEFAULT 1,
	note          TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_users_last_seen ON users (last_seen_at DESC);`,
	},
	{
		// Only a room's definition is persistent. Everything that happens
		// inside it — messages, files, drawings — stays in memory and is
		// dropped when the room empties.
		name: "0004_rooms",
		stmt: `
CREATE TABLE rooms (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	password   TEXT NOT NULL DEFAULT '',
	capacity   INTEGER NOT NULL,
	position   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX idx_rooms_name ON rooms (name COLLATE NOCASE);`,
	},
	{
		// Roles carry a permission bitmask and a priority. A moderator may only
		// act on users whose priority is strictly lower than their own, which
		// is what keeps two admins from banning each other.
		//
		// The two seeded roles are what makes a fresh server usable: everyone
		// implicitly gets the default role, and the panel hands the admin role
		// to the first real administrator.
		name: "0005_roles_and_sanctions",
		stmt: `
CREATE TABLE roles (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	permissions INTEGER NOT NULL DEFAULT 0,
	priority    INTEGER NOT NULL DEFAULT 0,
	color       TEXT NOT NULL DEFAULT '',
	is_default  INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX idx_roles_name ON roles (name COLLATE NOCASE);

CREATE TABLE user_roles (
	client_uuid TEXT NOT NULL,
	role_id     TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
	granted_by  TEXT NOT NULL DEFAULT '',
	granted_at  INTEGER NOT NULL,
	PRIMARY KEY (client_uuid, role_id)
) STRICT;

-- Bans and mutes share a table: they differ only in what they take away.
-- expires_at = 0 means permanent.
CREATE TABLE sanctions (
	id          TEXT PRIMARY KEY,
	client_uuid TEXT NOT NULL,
	kind        TEXT NOT NULL,
	reason      TEXT NOT NULL DEFAULT '',
	username    TEXT NOT NULL DEFAULT '',
	created_by  TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE UNIQUE INDEX idx_sanctions_target ON sanctions (client_uuid, kind);

-- What moderators did, so the panel can show it and nothing happens silently.
CREATE TABLE mod_log (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	at          INTEGER NOT NULL,
	actor_uuid  TEXT NOT NULL DEFAULT '',
	actor_name  TEXT NOT NULL DEFAULT '',
	action      TEXT NOT NULL,
	target_uuid TEXT NOT NULL DEFAULT '',
	target_name TEXT NOT NULL DEFAULT '',
	detail      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_mod_log_at ON mod_log (at DESC);

ALTER TABLE rooms ADD COLUMN required_role_id TEXT NOT NULL DEFAULT '';`,
	},
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	name       TEXT PRIMARY KEY,
	applied_at INTEGER NOT NULL
) STRICT;`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, m := range migrations {
		if applied[m.name] {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, m.stmt); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, unixepoch())`, m.name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
		slog.Info("migration applied", "name", m.name)
	}
	return nil
}
