package gateway

import (
	"context"
	"encoding/json"

	"tamizchat/internal/access"
	"tamizchat/internal/authz"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
)

// roleView converts a stored role into its public form.
func roleView(r storage.Role) protocol.Role {
	return protocol.Role{
		ID:          r.ID,
		Name:        r.Name,
		Permissions: authz.Keys(authz.Permission(r.Permissions)),
		Priority:    r.Priority,
		Color:       r.Color,
		IsDefault:   r.IsDefault,
	}
}

func (g *Gateway) roleViews() []protocol.Role {
	roles := g.access.AllRoles()
	out := make([]protocol.Role, 0, len(roles))
	for _, r := range roles {
		out = append(out, roleView(r))
	}
	return out
}

func (g *Gateway) handleRoleList(sess *session.Session, env protocol.Envelope) {
	// Every client needs the role list to render names and colours, so this is
	// readable without the manage-roles permission. Nothing secret is in it.
	_ = sess.SendMessage(protocol.TypeAdminRoles, env.ID, g.roleViews())
}

func (g *Gateway) handleRoleCreate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageRoles) {
		return
	}
	spec, ok := g.decodeRoleSpec(sess, env)
	if !ok {
		return
	}

	// Nobody may mint a role at or above their own rank, or one carrying
	// permissions they do not hold themselves — either would be a way to
	// escalate past whoever appointed them.
	if !g.roleWithinReach(sess, env, spec) {
		return
	}

	role, err := g.access.CreateRole(ctx, spec)
	if err != nil {
		g.replyAccessError(sess, env.ID, err)
		return
	}

	view := roleView(role)
	g.sessions.Broadcast(protocol.TypeAdminRole, view, sess.ClientUUID)
	_ = sess.SendMessage(protocol.TypeAdminRole, env.ID, view)
}

func (g *Gateway) handleRoleUpdate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageRoles) {
		return
	}

	var req protocol.RoleSpec
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}
	spec, ok := g.decodeRoleSpec(sess, env)
	if !ok {
		return
	}
	if !g.roleWithinReach(sess, env, spec) {
		return
	}

	role, err := g.access.UpdateRole(ctx, req.RoleID, spec)
	if err != nil {
		g.replyAccessError(sess, env.ID, err)
		return
	}

	view := roleView(role)
	g.sessions.Broadcast(protocol.TypeAdminRole, view, sess.ClientUUID)
	_ = sess.SendMessage(protocol.TypeAdminRole, env.ID, view)
	g.refreshRoles()
}

func (g *Gateway) handleRoleDelete(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageRoles) {
		return
	}

	var req protocol.RoleRef
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}
	if err := g.access.DeleteRole(ctx, req.RoleID); err != nil {
		g.replyAccessError(sess, env.ID, err)
		return
	}

	g.sessions.Broadcast(protocol.TypeAdminRoleGone, req, sess.ClientUUID)
	_ = sess.SendMessage(protocol.TypeAdminRoleGone, env.ID, req)
	g.refreshRoles()
}

func (g *Gateway) handleRoleGrant(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	g.changeAssignment(ctx, sess, env, true)
}

func (g *Gateway) handleRoleRevoke(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	g.changeAssignment(ctx, sess, env, false)
}

func (g *Gateway) changeAssignment(ctx context.Context, sess *session.Session, env protocol.Envelope, grant bool) {
	if !g.require(sess, env.ID, authz.PermManageRoles) {
		return
	}

	var req protocol.RoleAssignment
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	var err error
	action := "role_revoke"
	if grant {
		action = "role_grant"
		err = g.access.Grant(ctx, sess.ClientUUID, req.ClientUUID, req.RoleID)
	} else {
		err = g.access.Revoke(ctx, sess.ClientUUID, req.ClientUUID, req.RoleID)
	}
	if err != nil {
		g.replyAccessError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: action, TargetUUID: req.ClientUUID, Detail: req.RoleID,
	})

	g.announceRoles(req.ClientUUID)
	_ = sess.SendMessage(protocol.TypeAdminOK, env.ID, req)
}

// decodeRoleSpec turns the wire payload into the access package's spec.
func (g *Gateway) decodeRoleSpec(sess *session.Session, env protocol.Envelope) (access.RoleSpec, bool) {
	var req protocol.RoleSpec
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return access.RoleSpec{}, false
	}

	spec := access.RoleSpec{Name: req.Name, Priority: req.Priority, Color: req.Color}
	if req.Permissions != nil {
		mask := authz.Mask(*req.Permissions)
		spec.Permissions = &mask
	}
	return spec, true
}

// roleWithinReach stops an administrator from creating or editing a role that
// would outrank them or hand out permissions they do not have.
func (g *Gateway) roleWithinReach(sess *session.Session, env protocol.Envelope, spec access.RoleSpec) bool {
	if spec.Priority != nil && *spec.Priority >= g.policy.Priority(sess.ClientUUID) {
		sess.SendError(env.ID, protocol.ErrOutranked,
			"نمی‌توانید رولی هم‌رتبه یا بالاتر از خودتان بسازید")
		return false
	}
	if spec.Permissions != nil {
		mine := g.permissionsOf(sess.ClientUUID)
		if *spec.Permissions&^mine != 0 {
			sess.SendError(env.ID, protocol.ErrForbidden,
				"نمی‌توانید مجوزی بدهید که خودتان ندارید")
			return false
		}
	}
	return true
}

// announceRoles republishes a user's roles after they change, both to the user
// and to everyone who renders them.
func (g *Gateway) announceRoles(clientUUID string) {
	roles := g.access.Roles(clientUUID)
	ids := make([]string, 0, len(roles))
	for _, r := range roles {
		ids = append(ids, r.ID)
	}

	event := protocol.UserRoles{
		ClientUUID:  clientUUID,
		Roles:       ids,
		Permissions: authz.Keys(g.permissionsOf(clientUUID)),
	}

	// The affected user gets the detailed event, because only they need the
	// expanded permission list. Everyone else gets user.updated, which already
	// carries the role ids they render — one event per observer, no duplicates.
	if target, ok := g.sessions.Get(clientUUID); ok {
		target.SetRoles(ids)
		_ = target.SendMessage(protocol.TypeUserRolesChanged, "", event)
		g.sessions.Broadcast(protocol.TypeUserUpdated, target.User(), clientUUID)
	}
}

// refreshRoles re-caches every connected user's roles, used after a role
// definition changed in a way that affects who holds what.
func (g *Gateway) refreshRoles() {
	for _, sess := range g.sessions.Sessions() {
		g.applyRoles(sess)
	}
}

// applyRoles copies the current roles and mute state onto a session.
func (g *Gateway) applyRoles(sess *session.Session) {
	roles := g.access.Roles(sess.ClientUUID)
	ids := make([]string, 0, len(roles))
	for _, r := range roles {
		ids = append(ids, r.ID)
	}
	sess.SetRoles(ids)
	sess.SetMuted(g.access.IsMuted(sess.ClientUUID))
}

func (g *Gateway) permissionsOf(clientUUID string) authz.Permission {
	var mask authz.Permission
	for _, d := range authz.All {
		if g.policy.Can(clientUUID, d.Perm) {
			mask |= d.Perm
		}
	}
	return mask
}
