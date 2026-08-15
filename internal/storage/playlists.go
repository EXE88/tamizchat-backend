package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrPlaylistNotFound is returned when no playlist has the requested id.
var ErrPlaylistNotFound = errors.New("playlist not found")

// ErrPlaylistNameTaken is returned when the bot already has a playlist by that
// name. Names are unique per bot, not per server: two bots may both have a
// "Party" playlist.
var ErrPlaylistNameTaken = errors.New("playlist name already used")

// Playlist is a named group of tracks belonging to one bot. The tracks
// themselves are files on disk, not rows: what is playable is whatever is in
// the folder, which is also what a bot's folder has always meant.
type Playlist struct {
	ID        string
	BotID     string
	Name      string
	CreatedAt int64
}

// CreatePlaylist inserts a playlist.
func (s *Store) CreatePlaylist(ctx context.Context, p Playlist) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bot_playlists (id, bot_id, name, created_at)
VALUES (?, ?, ?, unixepoch())`, p.ID, p.BotID, p.Name)
	if isUniqueViolation(err) {
		return ErrPlaylistNameTaken
	}
	if err != nil {
		return fmt.Errorf("create playlist: %w", err)
	}
	return nil
}

// RenamePlaylist is the only edit a playlist has.
func (s *Store) RenamePlaylist(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE bot_playlists SET name = ? WHERE id = ?`, name, id)
	if isUniqueViolation(err) {
		return ErrPlaylistNameTaken
	}
	if err != nil {
		return fmt.Errorf("rename playlist: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPlaylistNotFound
	}
	return nil
}

// DeletePlaylist removes a playlist row. Its folder is the caller's business.
func (s *Store) DeletePlaylist(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM bot_playlists WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete playlist: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPlaylistNotFound
	}
	return nil
}

// DeletePlaylistsOfBot clears up after a deleted bot.
func (s *Store) DeletePlaylistsOfBot(ctx context.Context, botID string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM bot_playlists WHERE bot_id = ?`, botID); err != nil {
		return fmt.Errorf("delete playlists of bot: %w", err)
	}
	return nil
}

// GetPlaylist loads one playlist.
func (s *Store) GetPlaylist(ctx context.Context, id string) (Playlist, error) {
	var p Playlist
	err := s.db.QueryRowContext(ctx, `
SELECT id, bot_id, name, created_at FROM bot_playlists WHERE id = ?`, id).
		Scan(&p.ID, &p.BotID, &p.Name, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Playlist{}, ErrPlaylistNotFound
	}
	if err != nil {
		return Playlist{}, fmt.Errorf("get playlist: %w", err)
	}
	return p, nil
}

// ListPlaylists returns every playlist, oldest first, for every bot.
func (s *Store) ListPlaylists(ctx context.Context) ([]Playlist, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, bot_id, name, created_at FROM bot_playlists ORDER BY bot_id, created_at`)
	if err != nil {
		return nil, fmt.Errorf("list playlists: %w", err)
	}
	defer rows.Close()

	var out []Playlist
	for rows.Next() {
		var p Playlist
		if err := rows.Scan(&p.ID, &p.BotID, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
