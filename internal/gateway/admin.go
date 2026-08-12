package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"tamizchat/internal/access"
	"tamizchat/internal/authz"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
)

// broadcastExcept queues a frame for everyone except the listed clients.
// Moderation has two clients who must not receive the public announcement: the
// victim, who was told directly, and the moderator, whose reply is correlated
// to their request.
func (g *Gateway) broadcastExcept(typ string, payload any, exclude ...string) {
	frame, err := protocol.Encode(typ, "", payload)
	if err != nil {
		slog.Error("encode moderation event", "type", typ, "err", err)
		return
	}

	skip := make(map[string]bool, len(exclude))
	for _, uuid := range exclude {
		skip[uuid] = true
	}
	for _, s := range g.sessions.Sessions() {
		if skip[s.ClientUUID] {
			continue
		}
		_ = s.Send(frame)
	}
}

// require checks a permission and reports the failure to the caller.
func (g *Gateway) require(sess *session.Session, id string, perm authz.Permission) bool {
	if g.policy.Can(sess.ClientUUID, perm) {
		return true
	}
	sess.SendError(id, protocol.ErrForbidden, "you are not allowed to do that")
	return false
}

// target decodes a moderation request and resolves the victim's session.
// Moderation only works on connected users: an offline user has no session to
// kick, and banning by hand is the panel's job.
func (g *Gateway) target(sess *session.Session, env protocol.Envelope) (protocol.AdminTarget, *session.Session, bool) {
	var req protocol.AdminTarget
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return req, nil, false
	}

	victim, ok := g.sessions.Get(req.ClientUUID)
	if !ok {
		sess.SendError(env.ID, protocol.ErrUserNotFound, "no such user is online")
		return req, nil, false
	}
	return req, victim, true
}

func (g *Gateway) handleAdminKick(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermKick) {
		return
	}
	req, victim, ok := g.target(sess, env)
	if !ok {
		return
	}
	if victim.ClientUUID == sess.ClientUUID {
		sess.SendError(env.ID, protocol.ErrInvalidInput, "you cannot kick yourself")
		return
	}
	if g.access.OutranksOrEqual(sess.ClientUUID, victim.ClientUUID) {
		sess.SendError(env.ID, protocol.ErrOutranked, "that user ranks equal to or above you")
		return
	}

	event := protocol.Moderation{
		ClientUUID: victim.ClientUUID,
		Username:   victim.Username(),
		ByUUID:     sess.ClientUUID,
		ByUsername: sess.Username(),
		Reason:     req.Reason,
	}

	// The media session goes with them: a kicked user must not keep talking.
	g.dropFromMedia(victim)

	// The victim is told first, then everyone else, then the socket goes down.
	_ = victim.SendMessage(protocol.TypeUserKicked, "", event)
	g.broadcastExcept(protocol.TypeUserKicked, event, victim.ClientUUID, sess.ClientUUID)
	victim.Close(protocol.ReasonKicked)

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action:     "kick",
		TargetUUID: victim.ClientUUID, TargetName: victim.Username(),
		Detail: req.Reason,
	})
	_ = sess.SendMessage(protocol.TypeAdminOK, env.ID, event)
	slog.Info("user kicked", "target", victim.ClientUUID, "by", sess.ClientUUID, "reason", req.Reason)
}

func (g *Gateway) handleAdminBan(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermBan) {
		return
	}
	req, victim, ok := g.target(sess, env)
	if !ok {
		return
	}

	sanction, err := g.access.Ban(ctx, sess.ClientUUID, victim.ClientUUID,
		victim.Username(), req.Reason, time.Duration(req.DurationSec)*time.Second)
	if err != nil {
		g.replyAccessError(sess, env.ID, err)
		return
	}

	event := protocol.Moderation{
		ClientUUID: victim.ClientUUID,
		Username:   victim.Username(),
		ByUUID:     sess.ClientUUID,
		ByUsername: sess.Username(),
		Reason:     req.Reason,
		ExpiresAt:  sanction.ExpiresAt,
	}

	g.dropFromMedia(victim)
	_ = victim.SendMessage(protocol.TypeUserBanned, "", event)
	g.broadcastExcept(protocol.TypeUserBanned, event, victim.ClientUUID, sess.ClientUUID)
	victim.Close(protocol.ReasonBanned)

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action:     "ban",
		TargetUUID: victim.ClientUUID, TargetName: victim.Username(),
		Detail: req.Reason,
	})
	_ = sess.SendMessage(protocol.TypeAdminOK, env.ID, event)
	slog.Info("user banned", "target", victim.ClientUUID, "by", sess.ClientUUID,
		"until", sanction.ExpiresAt, "reason", req.Reason)
}

func (g *Gateway) handleAdminUnban(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	g.liftSanction(ctx, sess, env, storage.SanctionBan, authz.PermBan)
}

func (g *Gateway) handleAdminUnmute(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	g.liftSanction(ctx, sess, env, storage.SanctionMute, authz.PermMute)
}

// liftSanction removes a ban or a mute. Unlike the other moderation actions
// this one works on users who are not connected — an offline ban is exactly
// the kind that needs lifting.
func (g *Gateway) liftSanction(ctx context.Context, sess *session.Session, env protocol.Envelope,
	kind string, perm authz.Permission) {
	if !g.require(sess, env.ID, perm) {
		return
	}

	var req protocol.AdminTarget
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	removed, err := g.access.Lift(ctx, kind, req.ClientUUID)
	if err != nil {
		g.replyAccessError(sess, env.ID, err)
		return
	}
	if !removed {
		sess.SendError(env.ID, protocol.ErrUserNotFound, "no such sanction is recorded for that user")
		return
	}

	action := "unban"
	if kind == storage.SanctionMute {
		action = "unmute"
	}
	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: action, TargetUUID: req.ClientUUID,
	})

	if kind == storage.SanctionMute {
		event := protocol.Moderation{
			ClientUUID: req.ClientUUID,
			ByUUID:     sess.ClientUUID,
			ByUsername: sess.Username(),
		}
		if victim, ok := g.sessions.Get(req.ClientUUID); ok {
			victim.SetMuted(false)
			event.Username = victim.Username()
			_ = victim.SendMessage(protocol.TypeUserUnmuted, "", event)
		}
		g.broadcastExcept(protocol.TypeUserUnmuted, event, req.ClientUUID, sess.ClientUUID)
	}

	_ = sess.SendMessage(protocol.TypeAdminOK, env.ID, protocol.AdminTarget{ClientUUID: req.ClientUUID})
}

func (g *Gateway) handleAdminMute(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermMute) {
		return
	}
	req, victim, ok := g.target(sess, env)
	if !ok {
		return
	}

	sanction, err := g.access.Mute(ctx, sess.ClientUUID, victim.ClientUUID,
		victim.Username(), req.Reason, time.Duration(req.DurationSec)*time.Second)
	if err != nil {
		g.replyAccessError(sess, env.ID, err)
		return
	}
	victim.SetMuted(true)
	// A mute has to reach LiveKit, or the muted user keeps speaking over
	// everyone while their chat is silenced.
	g.syncMedia(victim.ClientUUID)

	event := protocol.Moderation{
		ClientUUID: victim.ClientUUID,
		Username:   victim.Username(),
		ByUUID:     sess.ClientUUID,
		ByUsername: sess.Username(),
		Reason:     req.Reason,
		ExpiresAt:  sanction.ExpiresAt,
	}
	_ = victim.SendMessage(protocol.TypeUserMuted, "", event)
	g.broadcastExcept(protocol.TypeUserMuted, event, victim.ClientUUID, sess.ClientUUID)

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action:     "mute",
		TargetUUID: victim.ClientUUID, TargetName: victim.Username(),
		Detail: req.Reason,
	})
	_ = sess.SendMessage(protocol.TypeAdminOK, env.ID, event)
}

func (g *Gateway) handleAdminMove(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermMoveUsers) {
		return
	}

	var req protocol.AdminMove
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}
	victim, ok := g.sessions.Get(req.ClientUUID)
	if !ok {
		sess.SendError(env.ID, protocol.ErrUserNotFound, "no such user is online")
		return
	}
	if victim.ClientUUID != sess.ClientUUID &&
		g.access.OutranksOrEqual(sess.ClientUUID, victim.ClientUUID) {
		sess.SendError(env.ID, protocol.ErrOutranked, "that user ranks equal to or above you")
		return
	}

	// Moving someone out of every room is expressed as an empty room id.
	if req.RoomID == "" {
		if _, err := g.rooms.Leave(victim); err != nil {
			g.replyRoomError(sess, env.ID, err)
			return
		}
		_ = victim.SendMessage(protocol.TypeRoomLeft, "", protocol.RoomLeft{Reason: protocol.ReasonMoved})
		_ = sess.SendMessage(protocol.TypeAdminOK, env.ID, req)
		return
	}

	// The room's own password and role locks do not apply: an admin placing
	// someone is an explicit decision that already passed a permission check.
	room, err := g.rooms.Force(victim, req.RoomID)
	if err != nil {
		g.replyRoomError(sess, env.ID, err)
		return
	}

	_ = victim.SendMessage(protocol.TypeRoomJoined, "", protocol.RoomJoined{Room: room.View(true)})
	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action:     "move",
		TargetUUID: victim.ClientUUID, TargetName: victim.Username(),
		Detail: room.Name(),
	})
	_ = sess.SendMessage(protocol.TypeAdminOK, env.ID, req)
}

func (g *Gateway) handleAdminSanctions(sess *session.Session, env protocol.Envelope) {
	if !g.policy.Can(sess.ClientUUID, authz.PermBan) &&
		!g.policy.Can(sess.ClientUUID, authz.PermMute) {
		sess.SendError(env.ID, protocol.ErrForbidden, "you are not allowed to do that")
		return
	}

	records := g.access.ActiveSanctions()
	list := make([]protocol.Sanction, 0, len(records))
	for _, r := range records {
		list = append(list, protocol.Sanction{
			ClientUUID: r.ClientUUID,
			Username:   r.Username,
			Kind:       r.Kind,
			Reason:     r.Reason,
			CreatedBy:  r.CreatedBy,
			CreatedAt:  r.CreatedAt,
			ExpiresAt:  r.ExpiresAt,
		})
	}
	_ = sess.SendMessage(protocol.TypeAdminSanctionList, env.ID, protocol.SanctionList{Sanctions: list})
}

// replyAccessError maps an access-control error onto a protocol error code.
func (g *Gateway) replyAccessError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, access.ErrOutranked):
		sess.SendError(id, protocol.ErrOutranked, "that user ranks equal to or above you")
	case errors.Is(err, access.ErrRoleNotFound):
		sess.SendError(id, protocol.ErrRoleNotFound, "no such role exists")
	case errors.Is(err, access.ErrRoleNameTaken):
		sess.SendError(id, protocol.ErrRoleNameTaken, "a role with that name already exists")
	case errors.Is(err, access.ErrRoleProtected):
		sess.SendError(id, protocol.ErrRoleProtected, "that is a built-in role and cannot be deleted")
	default:
		var invalid *access.ValidationError
		if errors.As(err, &invalid) {
			sess.SendError(id, protocol.ErrInvalidInput, invalid.Msg)
			return
		}
		slog.Error("admin action failed", "client_uuid", sess.ClientUUID, "err", err)
		sess.SendError(id, protocol.ErrInternal, "internal server error")
	}
}
