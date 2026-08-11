package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"

	"tamizchat/internal/paint"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
)

func (g *Gateway) handlePaintBegin(sess *session.Session, env protocol.Envelope) {
	var req protocol.PaintBegin
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	stroke, err := g.paint.Begin(sess, req)
	if err != nil {
		g.replyPaintError(sess, env.ID, err)
		return
	}
	// The author needs the id the server assigned before it can append to it.
	_ = sess.SendMessage(protocol.TypePaintStarted, env.ID, stroke)
}

func (g *Gateway) handlePaintAppend(sess *session.Session, env protocol.Envelope) {
	var req protocol.PaintAppend
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	// Appends are the high-frequency part of drawing and the client already
	// drew the points locally, so there is no reply: only failures are worth a
	// frame, and a dropped point is not worth interrupting the drawing for.
	if err := g.paint.Append(sess, req); err != nil &&
		!errors.Is(err, paint.ErrRateLimited) && !errors.Is(err, paint.ErrNotFound) {
		g.replyPaintError(sess, env.ID, err)
	}
}

func (g *Gateway) handlePaintEnd(sess *session.Session, env protocol.Envelope) {
	var req protocol.PaintEnd
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}
	if err := g.paint.End(sess, req); err != nil {
		g.replyPaintError(sess, env.ID, err)
	}
}

func (g *Gateway) handlePaintUndo(sess *session.Session, env protocol.Envelope) {
	event, err := g.paint.Undo(sess)
	if err != nil {
		g.replyPaintError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypePaintUndone, env.ID, event)
}

func (g *Gateway) handlePaintClear(sess *session.Session, env protocol.Envelope) {
	var req protocol.PaintClear
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &req); err != nil {
			sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
			return
		}
	}

	event, err := g.paint.Clear(sess, req)
	if err != nil {
		g.replyPaintError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypePaintCleared, env.ID, event)
}

func (g *Gateway) handlePaintState(sess *session.Session, env protocol.Envelope) {
	state, err := g.paint.State(sess)
	if err != nil {
		g.replyPaintError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypePaintSnapshot, env.ID, state)
}

func (g *Gateway) replyPaintError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, paint.ErrDisabled):
		sess.SendError(id, protocol.ErrPaintDisabled, "تختهٔ نقاشی در این سرور فعال نیست")
	case errors.Is(err, paint.ErrNotInRoom):
		sess.SendError(id, protocol.ErrNotInRoom, "برای نقاشی باید داخل یک روم باشید")
	case errors.Is(err, paint.ErrForbidden):
		sess.SendError(id, protocol.ErrForbidden, "اجازهٔ این کار روی تخته را ندارید")
	case errors.Is(err, paint.ErrMuted):
		sess.SendError(id, protocol.ErrMuted, "شما میوت شده‌اید و نمی‌توانید نقاشی کنید")
	case errors.Is(err, paint.ErrRateLimited):
		sess.SendError(id, protocol.ErrTooFast, "سرعت ارسال نقاشی بیش از حد مجاز است")
	case errors.Is(err, paint.ErrFull):
		sess.SendError(id, protocol.ErrPaintFull, "تخته پر شده — برای ادامه آن را پاک کنید")
	case errors.Is(err, paint.ErrNotFound), errors.Is(err, paint.ErrNotYours):
		sess.SendError(id, protocol.ErrPaintNotFound, "این خط روی تخته نیست")
	default:
		var invalid *paint.ValidationError
		if errors.As(err, &invalid) {
			sess.SendError(id, protocol.ErrPaintInvalid, invalid.Msg)
			return
		}
		slog.Error("paint operation failed", "client_uuid", sess.ClientUUID, "err", err)
		sess.SendError(id, protocol.ErrInternal, "خطای داخلی سرور")
	}
}
