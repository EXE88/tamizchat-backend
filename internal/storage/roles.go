package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Well-known ids of the two roles seeded on a fresh server. They are fixed
// strings rather than random ids so the panel, the docs and an operator poking
// at the database all refer to the same thing.
const (
	RoleIDAdmin   = "role-admin"
	RoleIDDefault = "role-default"
)

// ErrRoleNotFound is returned when no role has the requested id.
var ErrRoleNotFound = errors.New("role not found")

// ErrRoleNameTaken is returned when another role already uses that name.
var ErrRoleNameTaken = errors.New("role name already used")

// Role is a named bundle of permissions.
type Role struct {
	ID          string
	Name        string
	Permissions uint64
	Priority    int
	Color       string
	// TagStyle is opaque JSON describing how the client draws this role's
	// tag. The server stores and returns it and never looks inside.
	TagStyle  string
	IsDefault bool // held implicitly by everyone
	CreatedAt int64
}

// CreateRole inserts a role.
func (s *Store) CreateRole(ctx context.Context, r Role) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO roles (id, name, permissions, priority, color, tag_style, is_default, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, unixepoch())`,
		r.ID, r.Name, int64(r.Permissions), r.Priority, r.Color, r.TagStyle, boolToInt(r.IsDefault))
	if isUniqueViolation(err) {
		return ErrRoleNameTaken
	}
	if err != nil {
		return fmt.Errorf("create role: %w", err)
	}
	return nil
}

// UpdateRole overwrites the editable fields of a role.
func (s *Store) UpdateRole(ctx context.Context, r Role) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE roles SET name = ?, permissions = ?, priority = ?, color = ?, tag_style = ?, is_default = ?
WHERE id = ?`,
		r.Name, int64(r.Permissions), r.Priority, r.Color, r.TagStyle, boolToInt(r.IsDefault), r.ID)
	if isUniqueViolation(err) {
		return ErrRoleNameTaken
	}
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRoleNotFound
	}
	return nil
}

// DeleteRole removes a role; its assignments go with it.
func (s *Store) DeleteRole(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRoleNotFound
	}
	return nil
}

// ListRoles returns every role, strongest first.
func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, permissions, priority, color, tag_style, is_default, created_at
FROM roles ORDER BY priority DESC, created_at`)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()

	var out []Role
	for rows.Next() {
		var r Role
		var perms int64
		var isDefault int
		if err := rows.Scan(&r.ID, &r.Name, &perms, &r.Priority, &r.Color, &r.TagStyle,
			&isDefault, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Permissions = uint64(perms)
		r.IsDefault = isDefault != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRole loads one role.
func (s *Store) GetRole(ctx context.Context, id string) (Role, error) {
	var r Role
	var perms int64
	var isDefault int
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, permissions, priority, color, tag_style, is_default, created_at
FROM roles WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &perms, &r.Priority, &r.Color, &r.TagStyle, &isDefault, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Role{}, ErrRoleNotFound
	}
	if err != nil {
		return Role{}, fmt.Errorf("get role: %w", err)
	}
	r.Permissions = uint64(perms)
	r.IsDefault = isDefault != 0
	return r, nil
}

// CountRoles reports how many roles exist, used to decide whether a fresh
// server still needs seeding.
func (s *Store) CountRoles(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles`).Scan(&n)
	return n, err
}

// GrantRole assigns a role to a user. Granting twice is not an error.
func (s *Store) GrantRole(ctx context.Context, clientUUID, roleID, grantedBy string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO user_roles (client_uuid, role_id, granted_by, granted_at)
VALUES (?, ?, ?, unixepoch())
ON CONFLICT(client_uuid, role_id) DO NOTHING`, clientUUID, roleID, grantedBy)
	if err != nil {
		return fmt.Errorf("grant role: %w", err)
	}
	return nil
}

// RevokeRole removes an assignment.
func (s *Store) RevokeRole(ctx context.Context, clientUUID, roleID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM user_roles WHERE client_uuid = ? AND role_id = ?`, clientUUID, roleID)
	return err
}

// RoleAssignment is one row of the user_roles table.
type RoleAssignment struct {
	ClientUUID string
	RoleID     string
}

// ListRoleAssignments returns every explicit user/role pairing.
func (s *Store) ListRoleAssignments(ctx context.Context) ([]RoleAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT client_uuid, role_id FROM user_roles`)
	if err != nil {
		return nil, fmt.Errorf("list role assignments: %w", err)
	}
	defer rows.Close()

	var out []RoleAssignment
	for rows.Next() {
		var a RoleAssignment
		if err := rows.Scan(&a.ClientUUID, &a.RoleID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
