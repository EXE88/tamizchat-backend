package gateway

import (
	"log/slog"
	"time"

	"tamizchat/internal/avatars"
	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
)

// handleAvatarUploadRequest issues the ticket for a new profile picture.
//
// Same two steps as a room file and a bot track — permission over the socket,
// bytes over HTTP — and for the same reason: everything that can be refused is
// refused before a byte leaves the client.
//
// There is no permission check beyond the feature being enabled. A profile
// picture is about the person asking and nobody else: the ticket is bound to
// their own client id here, so a client cannot upload a picture for somebody
// else however it addresses the request.
func (g *Gateway) handleAvatarUploadRequest(sess *session.Session, env protocol.Envelope) {
	if g.avatars == nil || !g.cfg.Bool(config.KeyAvatarsEnabled) {
		sess.SendError(env.ID, protocol.ErrUploadsDisabled,
			"profile pictures are disabled on this server")
		return
	}

	token := g.avatars.Ticket(sess.ClientUUID)

	_ = sess.SendMessage(protocol.TypeAvatarUploadTicket, env.ID, protocol.AvatarUploadTicket{
		URL:       "/api/v1/avatar",
		Token:     token,
		ExpiresAt: time.Now().Add(2 * time.Minute).Unix(),
		MaxSize:   avatars.MaxUpload,
	})
}

// handleAvatarClear removes the caller's own picture.
func (g *Gateway) handleAvatarClear(sess *session.Session, env protocol.Envelope) {
	if g.avatars == nil {
		sess.SendError(env.ID, protocol.ErrUploadsDisabled,
			"profile pictures are disabled on this server")
		return
	}

	g.avatars.Remove(sess.ClientUUID)
	g.AnnounceAvatar(sess.ClientUUID, "")
	_ = sess.SendMessage(protocol.TypeUserUpdated, env.ID, sess.User())
}

// AnnounceAvatar records a new picture tag on the live session and tells
// everybody.
//
// It goes out as an ordinary user.updated, which every client already handles
// for renames — a picture is another thing about a user that changed, and
// inventing a second event for it would mean every client learning a new
// message to do the same thing with.
//
// Called from the HTTP upload handler as well as from clear, which is why it is
// exported: the bytes arrive on a different connection from the socket that
// asked for the ticket.
func (g *Gateway) AnnounceAvatar(clientUUID, tag string) {
	sess, ok := g.sessions.Get(clientUUID)
	if !ok {
		// They uploaded and dropped off. The picture is on disk and the tag is
		// read from the folder at their next connection, so nothing is lost.
		return
	}

	sess.SetAvatar(tag)
	g.sessions.Broadcast(protocol.TypeUserUpdated, sess.User(), clientUUID)
	_ = sess.SendMessage(protocol.TypeUserUpdated, "", sess.User())

	slog.Info("avatar changed", "client_uuid", clientUUID, "cleared", tag == "")
}
