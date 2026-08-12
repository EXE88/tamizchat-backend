// Package access is the server's role and sanction system: who holds which
// role, what that role permits, and who is currently banned or muted.
//
// Everything is mirrored in memory. Role changes are rare and permission checks
// are frequent — on every message — so the checks must never touch the disk.
package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"tamizchat/internal/authz"
	"tamizchat/internal/storage"
	"tamizchat/internal/textutil"
)

// Role name bounds.
const (
	RoleNameMinLen = 1
	RoleNameMaxLen = 32
)

// Errors the gateway maps onto protocol error codes.
var (
	ErrRoleNotFound  = errors.New("role not found")
	ErrRoleNameTaken = errors.New("role name already used")
	ErrRoleProtected = errors.New("built-in role cannot be removed")
	ErrOutranked     = errors.New("target outranks you")
)

// ValidationError carries a message that can be shown to the user as-is.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Store is the persistence this package needs; storage.Store satisfies it.
type Store interface {
	ListRoles(ctx context.Context) ([]storage.Role, error)
	CountRoles(ctx context.Context) (int, error)
	CreateRole(ctx context.Context, r storage.Role) error
	UpdateRole(ctx context.Context, r storage.Role) error
	DeleteRole(ctx context.Context, id string) error
	GrantRole(ctx context.Context, clientUUID, roleID, grantedBy string) error
	RevokeRole(ctx context.Context, clientUUID, roleID string) error
	ListRoleAssignments(ctx context.Context) ([]storage.RoleAssignment, error)

	PutSanction(ctx context.Context, s storage.Sanction) error
	DeleteSanction(ctx context.Context, clientUUID, kind string) (bool, error)
	ListSanctions(ctx context.Context) ([]storage.Sanction, error)
	PurgeExpiredSanctions(ctx context.Context) (int, error)

	AppendModLog(ctx context.Context, e storage.ModEntry) error
}

// Manager owns the in-memory view of roles, assignments and sanctions.
type Manager struct {
	store Store

	mu          sync.RWMutex
	roles       map[string]storage.Role
	assignments map[string]map[string]bool // client uuid -> role ids
	sanctions   map[string]map[string]storage.Sanction
}

// New loads the access control state, seeding the built-in roles on a server
// that has none yet.
func New(ctx context.Context, store Store) (*Manager, error) {
	m := &Manager{store: store}

	if err := m.seed(ctx); err != nil {
		return nil, err
	}
	if _, err := store.PurgeExpiredSanctions(ctx); err != nil {
		return nil, err
	}
	if err := m.reload(ctx); err != nil {
		return nil, err
	}

	m.mu.RLock()
	slog.Info("access control loaded",
		"roles", len(m.roles), "assignments", len(m.assignments), "sanctions", len(m.sanctions))
	m.mu.RUnlock()
	return m, nil
}

// seed creates the starting roles the first time the server runs. Without them
// a fresh server would have nobody able to do anything.
func (m *Manager) seed(ctx context.Context) error {
	n, err := m.store.CountRoles(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	roles := []storage.Role{{
		ID:   storage.RoleIDDefault,
		Name: "User",
		Permissions: uint64(authz.PermSendMessages | authz.PermUploadFiles |
			authz.PermSpeak | authz.PermPublishVideo | authz.PermShareScreen |
			authz.PermPaint),
		Priority:  0,
		IsDefault: true,
	}, {
		ID:          storage.RoleIDAdmin,
		Name:        "Admin",
		Permissions: uint64(authz.Everything()),
		Priority:    100,
		Color:       "#e74c3c",
	}}

	for _, r := range roles {
		if err := m.store.CreateRole(ctx, r); err != nil {
			return fmt.Errorf("seed role %s: %w", r.Name, err)
		}
	}
	slog.Info("built-in roles created", "default", storage.RoleIDDefault, "admin", storage.RoleIDAdmin)
	return nil
}

// Reload replaces the in-memory view from the database, so roles and bans the
// admin panel wrote reach a running server without a restart. It returns the
// number of roles now defined.
func (m *Manager) Reload(ctx context.Context) (int, error) {
	if _, err := m.store.PurgeExpiredSanctions(ctx); err != nil {
		return 0, err
	}
	if err := m.reload(ctx); err != nil {
		return 0, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.roles), nil
}

// reload replaces the in-memory view from the database.
func (m *Manager) reload(ctx context.Context) error {
	roles, err := m.store.ListRoles(ctx)
	if err != nil {
		return err
	}
	assignments, err := m.store.ListRoleAssignments(ctx)
	if err != nil {
		return err
	}
	sanctions, err := m.store.ListSanctions(ctx)
	if err != nil {
		return err
	}

	roleByID := make(map[string]storage.Role, len(roles))
	for _, r := range roles {
		roleByID[r.ID] = r
	}

	byUser := make(map[string]map[string]bool)
	for _, a := range assignments {
		if _, known := roleByID[a.RoleID]; !known {
			continue
		}
		if byUser[a.ClientUUID] == nil {
			byUser[a.ClientUUID] = make(map[string]bool)
		}
		byUser[a.ClientUUID][a.RoleID] = true
	}

	byTarget := make(map[string]map[string]storage.Sanction)
	for _, s := range sanctions {
		if byTarget[s.ClientUUID] == nil {
			byTarget[s.ClientUUID] = make(map[string]storage.Sanction)
		}
		byTarget[s.ClientUUID][s.Kind] = s
	}

	m.mu.Lock()
	m.roles = roleByID
	m.assignments = byUser
	m.sanctions = byTarget
	m.mu.Unlock()
	return nil
}

// Can implements authz.Policy.
func (m *Manager) Can(clientUUID string, perm authz.Permission) bool {
	return m.Permissions(clientUUID)&perm != 0
}

// Priority implements authz.Policy: the rank of the user's strongest role.
func (m *Manager) Priority(clientUUID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	priority := 0
	for _, r := range m.rolesOfLocked(clientUUID) {
		if r.Priority > priority {
			priority = r.Priority
		}
	}
	return priority
}

// Permissions is the union of every role the user holds, including the roles
// marked as default, which everyone has implicitly.
func (m *Manager) Permissions(clientUUID string) authz.Permission {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var mask uint64
	for _, r := range m.rolesOfLocked(clientUUID) {
		mask |= r.Permissions
	}
	return authz.Permission(mask)
}

// Roles lists the roles a user holds, strongest first.
func (m *Manager) Roles(clientUUID string) []storage.Role {
	m.mu.RLock()
	defer m.mu.RUnlock()

	roles := m.rolesOfLocked(clientUUID)
	sortRoles(roles)
	return roles
}

// rolesOfLocked collects the default roles plus the ones granted to this user.
// The caller must hold m.mu.
func (m *Manager) rolesOfLocked(clientUUID string) []storage.Role {
	var out []storage.Role
	granted := m.assignments[clientUUID]

	for id, r := range m.roles {
		if r.IsDefault || granted[id] {
			out = append(out, r)
		}
	}
	return out
}

// AllRoles returns every role defined on the server, strongest first.
func (m *Manager) AllRoles() []storage.Role {
	m.mu.RLock()
	out := make([]storage.Role, 0, len(m.roles))
	for _, r := range m.roles {
		out = append(out, r)
	}
	m.mu.RUnlock()

	sortRoles(out)
	return out
}

// HasRole reports whether a user holds a specific role, counting the default
// roles everyone has.
func (m *Manager) HasRole(clientUUID, roleID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if r, ok := m.roles[roleID]; ok && r.IsDefault {
		return true
	}
	return m.assignments[clientUUID][roleID]
}

// RoleExists reports whether a role id is defined.
func (m *Manager) RoleExists(roleID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.roles[roleID]
	return ok
}

// CanEnterRestricted implements rooms.Access: a user may enter a role-locked
// room if they hold the role, or if they are allowed to bypass such locks.
func (m *Manager) CanEnterRestricted(clientUUID, roleID string) bool {
	if roleID == "" {
		return true
	}
	return m.HasRole(clientUUID, roleID) || m.Can(clientUUID, authz.PermJoinLockedRooms)
}

// CanBypassPassword implements rooms.Access.
func (m *Manager) CanBypassPassword(clientUUID string) bool {
	return m.Can(clientUUID, authz.PermBypassRoomPassword)
}

// OutranksOrEqual reports whether the target is at least as strong as the
// actor, in which case the actor may not act on them.
func (m *Manager) OutranksOrEqual(actorUUID, targetUUID string) bool {
	return m.Priority(targetUUID) >= m.Priority(actorUUID)
}

func sortRoles(roles []storage.Role) {
	// Strongest first, then by name so the order is stable in the UI.
	for i := 1; i < len(roles); i++ {
		for j := i; j > 0; j-- {
			a, b := roles[j-1], roles[j]
			if a.Priority > b.Priority ||
				(a.Priority == b.Priority && strings.ToLower(a.Name) <= strings.ToLower(b.Name)) {
				break
			}
			roles[j-1], roles[j] = roles[j], roles[j-1]
		}
	}
}

// normalizeRoleName validates a role name.
func normalizeRoleName(raw string) (string, error) {
	name, err := textutil.NormalizeName(raw, "role name", RoleNameMinLen, RoleNameMaxLen)
	if err != nil {
		return "", &ValidationError{Msg: err.Error()}
	}
	return name, nil
}

// now is a variable so tests can control expiry without sleeping.
var now = time.Now
