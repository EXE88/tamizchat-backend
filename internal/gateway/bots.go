package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"tamizchat/internal/authz"
	"tamizchat/internal/bots"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
)

// handleBotList returns every bot with what it is doing now. It needs no
// permission: clients render bots in the room tree the same way they render
// people, and the reply says nothing a room member cannot already hear.
func (g *Gateway) handleBotList(sess *session.Session, env protocol.Envelope) {
	_ = sess.SendMessage(protocol.TypeBots, env.ID, protocol.BotList{Bots: g.bots.Views()})
}

func (g *Gateway) handleBotControl(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermControlBots) {
		return
	}

	var req protocol.BotControl
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.Control(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_" + req.Action, TargetName: view.Name, Detail: trackTitle(view),
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

func (g *Gateway) handleBotMove(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermControlBots) {
		return
	}

	var req protocol.BotMove
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.Move(ctx, sess.ClientUUID, req.BotID, req.RoomID)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_move", TargetName: view.Name, Detail: req.RoomID,
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

// handleBotCreate defines a new bot. This is a heavier job than driving one, so
// it takes manage_bots rather than control_bots: an operator can hand out the
// music controls without handing over the server's bot configuration.
func (g *Gateway) handleBotCreate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotSpec
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.Create(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_create", TargetName: view.Name,
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

func (g *Gateway) handleBotUpdate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotSpec
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.Update(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_update", TargetName: view.Name,
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

func (g *Gateway) handleBotDelete(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotRef
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	if err := g.bots.Delete(ctx, req.BotID); err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_delete", TargetName: req.BotID,
	})

	// The actor is excluded from the broadcast and correlates their own reply by
	// id — the same pattern every other administrative action here uses.
	g.sessions.Broadcast(protocol.TypeBotGone, req, sess.ClientUUID)
	_ = sess.SendMessage(protocol.TypeBotGone, env.ID, req)
}

func trackTitle(view protocol.Bot) string {
	if view.Track == nil {
		return ""
	}
	return view.Track.Title
}

func (g *Gateway) replyBotError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, bots.ErrNotFound):
		sess.SendError(id, protocol.ErrBotNotFound, "no such bot exists")
	case errors.Is(err, bots.ErrDisabled):
		sess.SendError(id, protocol.ErrBotDisabled, "that bot is disabled")
	case errors.Is(err, bots.ErrEmptyQueue):
		sess.SendError(id, protocol.ErrBotEmpty, "that bot's music folder is empty")
	case errors.Is(err, bots.ErrBadAction):
		sess.SendError(id, protocol.ErrBotAction, "that command is not recognised for a bot")
	case errors.Is(err, bots.ErrNameTaken):
		sess.SendError(id, protocol.ErrBotNameUsed, "another bot already uses that name")
	case errors.Is(err, bots.ErrTooManyBots):
		sess.SendError(id, protocol.ErrBotTooMany, "this server already has as many bots as it allows")
	case errors.Is(err, bots.ErrNoRoom):
		sess.SendError(id, protocol.ErrRoomNotFound, "move the bot into a room first")
	case errors.Is(err, bots.ErrNoMedia):
		sess.SendError(id, protocol.ErrMediaDisabled,
			"LiveKit must be configured and enabled to play music")
	default:
		var invalid *bots.ValidationError
		if errors.As(err, &invalid) {
			sess.SendError(id, protocol.ErrInvalidInput, invalid.Msg)
			return
		}
		slog.Error("bot operation failed", "client_uuid", sess.ClientUUID, "err", err)
		sess.SendError(id, protocol.ErrInternal, "internal server error")
	}
}
