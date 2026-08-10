package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrRoomNotFound is returned when no room has the requested id.
var ErrRoomNotFound = errors.New("room not found")

// ErrRoomNameTaken is returned when another room already uses that name.
var ErrRoomNameTaken = errors.New("room name already used")

// Room is the persistent definition of a room. What happens inside it is not
// stored anywhere.
type Room struct {
	ID        string
	Name      string
	Password  string // empty means the room is open
	Capacity  int
	Position  int // display order in the client's room list
	CreatedAt int64
	UpdatedAt int64
}

// CreateRoom inserts a room definition.
func (s *Store) CreateRoom(ctx context.Context, r Room) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO rooms (id, name, password, capacity, position, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, unixepoch(), unixepoch())`,
		r.ID, r.Name, r.Password, r.Capacity, r.Position)
	if isUniqueViolation(err) {
		return ErrRoomNameTaken
	}
	if err != nil {
		return fmt.Errorf("create room: %w", err)
	}
	return nil
}

// UpdateRoom overwrites the editable fields of an existing room.
func (s *Store) UpdateRoom(ctx context.Context, r Room) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE rooms SET name = ?, password = ?, capacity = ?, position = ?, updated_at = unixepoch()
WHERE id = ?`, r.Name, r.Password, r.Capacity, r.Position, r.ID)
	if isUniqueViolation(err) {
		return ErrRoomNameTaken
	}
	if err != nil {
		return fmt.Errorf("update room: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRoomNotFound
	}
	return nil
}

// DeleteRoom removes a room definition.
func (s *Store) DeleteRoom(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM rooms WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete room: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRoomNotFound
	}
	return nil
}

// GetRoom loads one room definition.
func (s *Store) GetRoom(ctx context.Context, id string) (Room, error) {
	var r Room
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, password, capacity, position, created_at, updated_at
FROM rooms WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Password, &r.Capacity, &r.Position, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrRoomNotFound
	}
	if err != nil {
		return Room{}, fmt.Errorf("get room: %w", err)
	}
	return r, nil
}

// ListRooms returns every room in display order.
func (s *Store) ListRooms(ctx context.Context) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, password, capacity, position, created_at, updated_at
FROM rooms ORDER BY position, created_at`)
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	defer rows.Close()

	var out []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.Name, &r.Password, &r.Capacity,
			&r.Position, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NextRoomPosition returns the position that puts a new room at the end.
func (s *Store) NextRoomPosition(ctx context.Context) (int, error) {
	var pos sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(position) FROM rooms`).Scan(&pos); err != nil {
		return 0, err
	}
	if !pos.Valid {
		return 0, nil
	}
	return int(pos.Int64) + 1, nil
}

// isUniqueViolation recognizes the driver's unique-constraint error without
// depending on the driver's concrete error type.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
