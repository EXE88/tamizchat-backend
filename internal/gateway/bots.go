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
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
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
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
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

func trackTitle(view protocol.Bot) string {
	if view.Track == nil {
		return ""
	}
	return view.Track.Title
}

func (g *Gateway) replyBotError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, bots.ErrNotFound):
		sess.SendError(id, protocol.ErrBotNotFound, "چنین باتی وجود ندارد")
	case errors.Is(err, bots.ErrDisabled):
		sess.SendError(id, protocol.ErrBotDisabled, "این بات غیرفعال است")
	case errors.Is(err, bots.ErrEmptyQueue):
		sess.SendError(id, protocol.ErrBotEmpty, "فولدر موسیقی این بات خالی است")
	case errors.Is(err, bots.ErrBadAction):
		sess.SendError(id, protocol.ErrBotAction, "این دستور برای بات شناخته نمی‌شود")
	case errors.Is(err, bots.ErrNoRoom):
		sess.SendError(id, protocol.ErrRoomNotFound, "اول بات را به یک روم منتقل کنید")
	case errors.Is(err, bots.ErrNoMedia):
		sess.SendError(id, protocol.ErrMediaDisabled,
			"برای پخش موسیقی باید LiveKit تنظیم و فعال باشد")
	default:
		var invalid *bots.ValidationError
		if errors.As(err, &invalid) {
			sess.SendError(id, protocol.ErrInvalidInput, invalid.Msg)
			return
		}
		slog.Error("bot operation failed", "client_uuid", sess.ClientUUID, "err", err)
		sess.SendError(id, protocol.ErrInternal, "خطای داخلی سرور")
	}
}
