package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"tamizchat/internal/authz"
	"tamizchat/internal/protocol"
	"tamizchat/internal/rooms"
	"tamizchat/internal/session"
)

func (g *Gateway) handleRoomList(sess *session.Session, env protocol.Envelope) {
	_ = sess.SendMessage(protocol.TypeRooms, env.ID, protocol.RoomList{Rooms: g.rooms.Views()})
}

func (g *Gateway) handleRoomJoin(sess *session.Session, env protocol.Envelope) {
	var req protocol.RoomJoin
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	room, err := g.rooms.Join(sess, req.RoomID, req.Password)
	if err != nil {
		g.replyRoomError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeRoomJoined, env.ID, protocol.RoomJoined{Room: room.View(true)})
}

func (g *Gateway) handleRoomLeave(sess *session.Session, env protocol.Envelope) {
	roomID, err := g.rooms.Leave(sess)
	if err != nil {
		g.replyRoomError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeRoomLeft, env.ID, protocol.RoomLeft{
		RoomID: roomID, Reason: protocol.ReasonLeftVoluntarily,
	})
}

func (g *Gateway) handleRoomCreate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageRooms) {
		return
	}

	var req protocol.RoomCreate
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	room, err := g.rooms.Create(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyRoomError(sess, env.ID, err)
		return
	}
	// The creator gets the reply correlated to the request; everyone else was
	// already told by the manager's broadcast.
	_ = sess.SendMessage(protocol.TypeRoomCreated, env.ID, room.View(false))
}

func (g *Gateway) handleRoomUpdate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageRooms) {
		return
	}

	var req protocol.RoomUpdate
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	room, err := g.rooms.Update(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyRoomError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeRoomUpdated, env.ID, room.View(false))
}

func (g *Gateway) handleRoomDelete(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageRooms) {
		return
	}

	var req protocol.RoomDelete
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	if err := g.rooms.Delete(ctx, sess.ClientUUID, req.RoomID); err != nil {
		g.replyRoomError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeRoomDeleted, env.ID, protocol.RoomRef{RoomID: req.RoomID})
}

// replyRoomError maps a manager error onto the protocol's error codes. Anything
// unrecognized is a validation message from the manager, which the client can
// show as-is.
func (g *Gateway) replyRoomError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, rooms.ErrNotFound):
		sess.SendError(id, protocol.ErrRoomNotFound, "no such room exists")
	case errors.Is(err, rooms.ErrNameTaken):
		sess.SendError(id, protocol.ErrRoomNameTaken, "a room with that name already exists")
	case errors.Is(err, rooms.ErrBadPassword):
		sess.SendError(id, protocol.ErrRoomPassword, "the room password is wrong")
	case errors.Is(err, rooms.ErrFull):
		sess.SendError(id, protocol.ErrRoomFull, "the room is full")
	case errors.Is(err, rooms.ErrLimitReached):
		sess.SendError(id, protocol.ErrRoomLimit, "you have reached the server's room limit")
	case errors.Is(err, rooms.ErrNotInRoom):
		sess.SendError(id, protocol.ErrNotInRoom, "you are not in a room")
	case errors.Is(err, rooms.ErrRoleRequired):
		sess.SendError(id, protocol.ErrRoleRequired, "you do not have the role required to join this room")
	case errors.Is(err, rooms.ErrRoleUnknown):
		sess.SendError(id, protocol.ErrRoleNotFound, "no such role exists")
	default:
		var invalid *rooms.ValidationError
		if errors.As(err, &invalid) {
			sess.SendError(id, protocol.ErrRoomInvalidName, invalid.Msg)
			return
		}
		slog.Error("room operation failed", "client_uuid", sess.ClientUUID, "err", err)
		sess.SendError(id, protocol.ErrInternal, "internal server error")
	}
}
