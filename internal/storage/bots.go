package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Bot kinds. Only music bots exist so far; the column is here so a second kind
// does not need a migration.
const BotKindMusic = "music"

// ErrBotNotFound is returned when no bot has the requested id.
var ErrBotNotFound = errors.New("bot not found")

// ErrBotNameTaken is returned when another bot already uses that name.
var ErrBotNameTaken = errors.New("bot name already used")

// Bot is the persistent configuration of a bot. What it is playing right now
// is runtime state and lives in memory.
type Bot struct {
	ID        string
	Name      string
	Kind      string
	Folder    string
	Color     string
	LoopQueue bool
	Shuffle   bool
	Enabled   bool
	// PlaylistID is the playlist the bot plays from. Empty means it plays
	// Folder itself, which is what a panel-configured bot does.
	PlaylistID string
	CreatedAt  int64
}

// CreateBot inserts a bot definition.
func (s *Store) CreateBot(ctx context.Context, b Bot) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bots (id, name, kind, folder, color, loop_queue, shuffle, enabled, playlist_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch())`,
		b.ID, b.Name, b.Kind, b.Folder, b.Color,
		boolToInt(b.LoopQueue), boolToInt(b.Shuffle), boolToInt(b.Enabled), b.PlaylistID)
	if isUniqueViolation(err) {
		return ErrBotNameTaken
	}
	if err != nil {
		return fmt.Errorf("create bot: %w", err)
	}
	return nil
}

// UpdateBot overwrites the editable fields of a bot.
func (s *Store) UpdateBot(ctx context.Context, b Bot) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE bots SET name = ?, folder = ?, color = ?, loop_queue = ?, shuffle = ?,
	enabled = ?, playlist_id = ?
WHERE id = ?`,
		b.Name, b.Folder, b.Color, boolToInt(b.LoopQueue),
		boolToInt(b.Shuffle), boolToInt(b.Enabled), b.PlaylistID, b.ID)
	if isUniqueViolation(err) {
		return ErrBotNameTaken
	}
	if err != nil {
		return fmt.Errorf("update bot: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBotNotFound
	}
	return nil
}

// DeleteBot removes a bot definition.
func (s *Store) DeleteBot(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM bots WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete bot: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBotNotFound
	}
	return nil
}

// GetBot loads one bot definition.
func (s *Store) GetBot(ctx context.Context, id string) (Bot, error) {
	var b Bot
	var loop, shuffle, enabled int
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, kind, folder, color, loop_queue, shuffle, enabled, playlist_id, created_at
FROM bots WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Kind, &b.Folder, &b.Color, &loop, &shuffle, &enabled,
			&b.PlaylistID, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Bot{}, ErrBotNotFound
	}
	if err != nil {
		return Bot{}, fmt.Errorf("get bot: %w", err)
	}
	b.LoopQueue, b.Shuffle, b.Enabled = loop != 0, shuffle != 0, enabled != 0
	return b, nil
}

// ListBots returns every bot, oldest first.
func (s *Store) ListBots(ctx context.Context) ([]Bot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, kind, folder, color, loop_queue, shuffle, enabled, playlist_id, created_at
FROM bots ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list bots: %w", err)
	}
	defer rows.Close()

	var out []Bot
	for rows.Next() {
		var b Bot
		var loop, shuffle, enabled int
		if err := rows.Scan(&b.ID, &b.Name, &b.Kind, &b.Folder, &b.Color,
			&loop, &shuffle, &enabled, &b.PlaylistID, &b.CreatedAt); err != nil {
			return nil, err
		}
		b.LoopQueue, b.Shuffle, b.Enabled = loop != 0, shuffle != 0, enabled != 0
		out = append(out, b)
	}
	return out, rows.Err()
}
