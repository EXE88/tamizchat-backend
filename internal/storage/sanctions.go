package storage

import (
	"context"
	"fmt"
)

// Sanction kinds.
const (
	SanctionBan  = "ban"
	SanctionMute = "mute"
)

// Sanction is a ban or a mute. ExpiresAt of 0 means permanent.
type Sanction struct {
	ID         string
	ClientUUID string
	Kind       string
	Reason     string
	Username   string // remembered so the panel can show who this was
	CreatedBy  string
	CreatedAt  int64
	ExpiresAt  int64
}

// PutSanction records a ban or mute, replacing any existing one of the same
// kind for that user — re-banning simply updates the reason and expiry.
func (s *Store) PutSanction(ctx context.Context, sanction Sanction) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sanctions (id, client_uuid, kind, reason, username, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, unixepoch(), ?)
ON CONFLICT(client_uuid, kind) DO UPDATE SET
	reason     = excluded.reason,
	username   = excluded.username,
	created_by = excluded.created_by,
	created_at = excluded.created_at,
	expires_at = excluded.expires_at`,
		sanction.ID, sanction.ClientUUID, sanction.Kind, sanction.Reason,
		sanction.Username, sanction.CreatedBy, sanction.ExpiresAt)
	if err != nil {
		return fmt.Errorf("put sanction: %w", err)
	}
	return nil
}

// DeleteSanction lifts a ban or mute. It reports whether anything was removed.
func (s *Store) DeleteSanction(ctx context.Context, clientUUID, kind string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sanctions WHERE client_uuid = ? AND kind = ?`, clientUUID, kind)
	if err != nil {
		return false, fmt.Errorf("delete sanction: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListSanctions returns every recorded sanction, newest first. Expired rows are
// included: the caller decides what to do with them.
func (s *Store) ListSanctions(ctx context.Context) ([]Sanction, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, client_uuid, kind, reason, username, created_by, created_at, expires_at
FROM sanctions ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sanctions: %w", err)
	}
	defer rows.Close()

	var out []Sanction
	for rows.Next() {
		var v Sanction
		if err := rows.Scan(&v.ID, &v.ClientUUID, &v.Kind, &v.Reason, &v.Username,
			&v.CreatedBy, &v.CreatedAt, &v.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// PurgeExpiredSanctions deletes sanctions whose time has passed, so the panel
// does not fill up with history nobody is enforcing.
func (s *Store) PurgeExpiredSanctions(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sanctions WHERE expires_at > 0 AND expires_at <= unixepoch()`)
	if err != nil {
		return 0, fmt.Errorf("purge sanctions: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ModEntry is one line of the moderation log.
type ModEntry struct {
	ID         int64
	At         int64
	ActorUUID  string
	ActorName  string
	Action     string
	TargetUUID string
	TargetName string
	Detail     string
}

// AppendModLog records a moderation action.
func (s *Store) AppendModLog(ctx context.Context, e ModEntry) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO mod_log (at, actor_uuid, actor_name, action, target_uuid, target_name, detail)
VALUES (unixepoch(), ?, ?, ?, ?, ?, ?)`,
		e.ActorUUID, e.ActorName, e.Action, e.TargetUUID, e.TargetName, e.Detail)
	if err != nil {
		return fmt.Errorf("append mod log: %w", err)
	}
	return nil
}

// RecentModLog returns the newest entries first.
func (s *Store) RecentModLog(ctx context.Context, limit int) ([]ModEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, at, actor_uuid, actor_name, action, target_uuid, target_name, detail
FROM mod_log ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read mod log: %w", err)
	}
	defer rows.Close()

	var out []ModEntry
	for rows.Next() {
		var e ModEntry
		if err := rows.Scan(&e.ID, &e.At, &e.ActorUUID, &e.ActorName, &e.Action,
			&e.TargetUUID, &e.TargetName, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
