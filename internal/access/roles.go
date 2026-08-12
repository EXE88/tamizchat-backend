package access

import (
	"context"
	"errors"
	"log/slog"

	"tamizchat/internal/authz"
	"tamizchat/internal/storage"
)

// RoleSpec describes a role to create or update. Nil fields are left unchanged
// on update.
type RoleSpec struct {
	Name        *string
	Permissions *authz.Permission
	Priority    *int
	Color       *string
}

// CreateRole defines a new role.
func (m *Manager) CreateRole(ctx context.Context, spec RoleSpec) (storage.Role, error) {
	if spec.Name == nil {
		return storage.Role{}, &ValidationError{Msg: "the role name is required"}
	}
	name, err := normalizeRoleName(*spec.Name)
	if err != nil {
		return storage.Role{}, err
	}

	role := storage.Role{ID: storage.NewUUID(), Name: name}
	if spec.Permissions != nil {
		role.Permissions = uint64(*spec.Permissions)
	}
	if spec.Priority != nil {
		role.Priority = *spec.Priority
	}
	if spec.Color != nil {
		role.Color = *spec.Color
	}

	if err := m.store.CreateRole(ctx, role); err != nil {
		if errors.Is(err, storage.ErrRoleNameTaken) {
			return storage.Role{}, ErrRoleNameTaken
		}
		return storage.Role{}, err
	}

	m.mu.Lock()
	m.roles[role.ID] = role
	m.mu.Unlock()

	slog.Info("role created", "role", role.Name, "id", role.ID, "priority", role.Priority)
	return role, nil
}

// UpdateRole edits a role definition.
func (m *Manager) UpdateRole(ctx context.Context, roleID string, spec RoleSpec) (storage.Role, error) {
	m.mu.RLock()
	role, ok := m.roles[roleID]
	m.mu.RUnlock()
	if !ok {
		return storage.Role{}, ErrRoleNotFound
	}

	if spec.Name != nil {
		name, err := normalizeRoleName(*spec.Name)
		if err != nil {
			return storage.Role{}, err
		}
		role.Name = name
	}
	if spec.Permissions != nil {
		role.Permissions = uint64(*spec.Permissions)
	}
	if spec.Priority != nil {
		role.Priority = *spec.Priority
	}
	if spec.Color != nil {
		role.Color = *spec.Color
	}

	if err := m.store.UpdateRole(ctx, role); err != nil {
		switch {
		case errors.Is(err, storage.ErrRoleNameTaken):
			return storage.Role{}, ErrRoleNameTaken
		case errors.Is(err, storage.ErrRoleNotFound):
			return storage.Role{}, ErrRoleNotFound
		}
		return storage.Role{}, err
	}

	m.mu.Lock()
	m.roles[roleID] = role
	m.mu.Unlock()

	slog.Info("role updated", "role", role.Name, "id", roleID)
	return role, nil
}

// DeleteRole removes a role and every assignment of it. The two built-in roles
// are protected: deleting the default role would leave newcomers with no
// permissions at all, and deleting the admin role could lock everyone out.
func (m *Manager) DeleteRole(ctx context.Context, roleID string) error {
	if roleID == storage.RoleIDDefault || roleID == storage.RoleIDAdmin {
		return ErrRoleProtected
	}

	m.mu.RLock()
	_, ok := m.roles[roleID]
	m.mu.RUnlock()
	if !ok {
		return ErrRoleNotFound
	}

	if err := m.store.DeleteRole(ctx, roleID); err != nil {
		if errors.Is(err, storage.ErrRoleNotFound) {
			return ErrRoleNotFound
		}
		return err
	}

	m.mu.Lock()
	delete(m.roles, roleID)
	for _, roles := range m.assignments {
		delete(roles, roleID)
	}
	m.mu.Unlock()

	slog.Info("role deleted", "id", roleID)
	return nil
}

// Grant gives a role to a user. A moderator may not hand out a role at or above
// their own rank — otherwise anyone able to manage roles could promote
// themselves past the person who appointed them. An empty actor is the admin
// panel, which is not subject to ranking: whoever runs it already has shell
// access to the server.
func (m *Manager) Grant(ctx context.Context, actorUUID, targetUUID, roleID string) error {
	m.mu.RLock()
	role, ok := m.roles[roleID]
	m.mu.RUnlock()
	if !ok {
		return ErrRoleNotFound
	}

	if actorUUID != "" && role.Priority >= m.Priority(actorUUID) {
		return ErrOutranked
	}

	if err := m.store.GrantRole(ctx, targetUUID, roleID, actorUUID); err != nil {
		return err
	}

	m.mu.Lock()
	if m.assignments[targetUUID] == nil {
		m.assignments[targetUUID] = make(map[string]bool)
	}
	m.assignments[targetUUID][roleID] = true
	m.mu.Unlock()

	slog.Info("role granted", "role", role.Name, "target", targetUUID, "by", actorUUID)
	return nil
}

// Revoke takes a role away from a user.
func (m *Manager) Revoke(ctx context.Context, actorUUID, targetUUID, roleID string) error {
	m.mu.RLock()
	role, ok := m.roles[roleID]
	m.mu.RUnlock()
	if !ok {
		return ErrRoleNotFound
	}

	if actorUUID != "" && m.OutranksOrEqual(actorUUID, targetUUID) {
		return ErrOutranked
	}

	if err := m.store.RevokeRole(ctx, targetUUID, roleID); err != nil {
		return err
	}

	m.mu.Lock()
	delete(m.assignments[targetUUID], roleID)
	if len(m.assignments[targetUUID]) == 0 {
		delete(m.assignments, targetUUID)
	}
	m.mu.Unlock()

	slog.Info("role revoked", "role", role.Name, "target", targetUUID, "by", actorUUID)
	return nil
}
