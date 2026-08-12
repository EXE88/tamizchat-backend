package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"

	"tamizchat/internal/media"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
)

// handleMediaToken hands out the credentials for the caller's current room.
// A client asks for one when it wants to join the voice session, and again
// whenever the previous token is about to expire.
func (g *Gateway) handleMediaToken(sess *session.Session, env protocol.Envelope) {
	creds, err := g.media.IssueToken(sess)
	if err != nil {
		g.replyMediaError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeMediaCredentials, env.ID, creds)
}

// handleMediaSetState records what the client has switched on and tells
// everyone, so the room tree can show microphone and camera icons.
//
// This is the client's own report. What it is *allowed* to switch on is decided
// by the server and enforced by LiveKit, so a client lying here gains nothing
// beyond a wrong icon.
func (g *Gateway) handleMediaSetState(sess *session.Session, env protocol.Envelope) {
	var state protocol.MediaState
	if err := json.Unmarshal(env.Data, &state); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	roomID := sess.RoomID()
	if roomID == "" {
		sess.SendError(env.ID, protocol.ErrNotInRoom, "you must be in a room to do that")
		return
	}

	sess.SetMedia(state)
	event := protocol.MediaStateEvent{
		ClientUUID: sess.ClientUUID,
		RoomID:     roomID,
		State:      state,
	}
	g.sessions.Broadcast(protocol.TypeMediaState, event, sess.ClientUUID)
	_ = sess.SendMessage(protocol.TypeMediaState, env.ID, event)
}

// syncMedia pushes a user's publishing rights to LiveKit after something
// changed them — a mute, an unmute, or a role change.
func (g *Gateway) syncMedia(clientUUID string) {
	if !g.media.Enabled() {
		return
	}
	sess, ok := g.sessions.Get(clientUUID)
	if !ok {
		return
	}
	if roomID := sess.RoomID(); roomID != "" {
		go g.media.SyncRights(clientUUID, roomID)
	}
}

// dropFromMedia ends a user's media session for the room they are in. Used when
// someone is kicked or banned: taking away the chat while leaving the
// microphone connected would be worse than useless.
func (g *Gateway) dropFromMedia(sess *session.Session) {
	if !g.media.Enabled() {
		return
	}
	if roomID := sess.RoomID(); roomID != "" {
		go g.media.Disconnect(sess.ClientUUID, roomID)
	}
}

func (g *Gateway) replyMediaError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, media.ErrDisabled):
		sess.SendError(id, protocol.ErrMediaDisabled, "voice and video are not enabled on this server")
	case errors.Is(err, media.ErrNotInRoom):
		sess.SendError(id, protocol.ErrNotInRoom, "you must be in a room to use voice")
	default:
		slog.Error("media token failed", "client_uuid", sess.ClientUUID, "err", err)
		sess.SendError(id, protocol.ErrInternal, "internal server error")
	}
}
