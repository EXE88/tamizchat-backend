package storage

import (
	"context"
	"fmt"
)

// KnownUser is the persisted record of someone who has connected before.
type KnownUser struct {
	ClientUUID  string
	Username    string
	FirstSeenAt int64
	LastSeenAt  int64
	VisitCount  int
	Note        string
}

// TouchUser records a connection: it creates the user on first sight and
// otherwise refreshes the name, the last-seen time and the visit counter.
func (s *Store) TouchUser(ctx context.Context, clientUUID, username string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (client_uuid, username, first_seen_at, last_seen_at, visit_count)
VALUES (?, ?, unixepoch(), unixepoch(), 1)
ON CONFLICT(client_uuid) DO UPDATE SET
	username     = excluded.username,
	last_seen_at = excluded.last_seen_at,
	visit_count  = users.visit_count + 1`,
		clientUUID, username)
	if err != nil {
		return fmt.Errorf("touch user: %w", err)
	}
	return nil
}

// UpdateUsername stores a rename without inflating the visit counter.
func (s *Store) UpdateUsername(ctx context.Context, clientUUID, username string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET username = ?, last_seen_at = unixepoch() WHERE client_uuid = ?`,
		username, clientUUID)
	return err
}

// MarkUserSeen refreshes last_seen_at, called when a session ends so the panel
// shows when someone was last around.
func (s *Store) MarkUserSeen(ctx context.Context, clientUUID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_seen_at = unixepoch() WHERE client_uuid = ?`, clientUUID)
	return err
}

// RecentUsers lists known users, most recently seen first.
func (s *Store) RecentUsers(ctx context.Context, limit int) ([]KnownUser, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT client_uuid, username, first_seen_at, last_seen_at, visit_count, note
FROM users ORDER BY last_seen_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var out []KnownUser
	for rows.Next() {
		var u KnownUser
		if err := rows.Scan(&u.ClientUUID, &u.Username, &u.FirstSeenAt,
			&u.LastSeenAt, &u.VisitCount, &u.Note); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers is the number of clients that have ever connected.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}
