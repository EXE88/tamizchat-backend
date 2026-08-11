package gateway

import (
	"encoding/json"
	"errors"

	"tamizchat/internal/chat"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
)

func (g *Gateway) handleChatSend(sess *session.Session, env protocol.Envelope) {
	var req protocol.ChatSend
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	msg, err := g.chat.Send(sess, req)
	if err != nil {
		g.replyChatError(sess, env.ID, err)
		return
	}
	// The sender's copy is correlated to the request, so the client can match
	// the message it drew optimistically with the one the server stored.
	_ = sess.SendMessage(protocol.TypeChatMessage, env.ID, msg)
}

func (g *Gateway) handleChatHistory(sess *session.Session, env protocol.Envelope) {
	var req protocol.ChatHistoryRequest
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &req); err != nil {
			sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
			return
		}
	}

	history, err := g.chat.History(sess, req)
	if err != nil {
		g.replyChatError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeChatHistoryReply, env.ID, history)
}

func (g *Gateway) handleChatEdit(sess *session.Session, env protocol.Envelope) {
	var req protocol.ChatEdit
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	msg, err := g.chat.Edit(sess, req)
	if err != nil {
		g.replyChatError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeChatUpdated, env.ID, msg)
}

func (g *Gateway) handleChatDelete(sess *session.Session, env protocol.Envelope) {
	var req protocol.ChatDelete
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	event, err := g.chat.Delete(sess, req)
	if err != nil {
		g.replyChatError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeChatDeleted, env.ID, event)
}

func (g *Gateway) handleChatTyping(sess *session.Session, env protocol.Envelope) {
	var req protocol.ChatTyping
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	// A typing hint that is dropped or rate limited is not worth an error
	// frame: the client's own timeout clears the indicator anyway.
	if err := g.chat.Typing(sess, req.Typing); err != nil &&
		!errors.Is(err, chat.ErrRateLimited) && !errors.Is(err, chat.ErrNotInRoom) &&
		!errors.Is(err, chat.ErrMuted) {
		g.replyChatError(sess, env.ID, err)
	}
}

func (g *Gateway) replyChatError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, chat.ErrNotInRoom):
		sess.SendError(id, protocol.ErrNotInRoom, "برای این کار باید داخل یک روم باشید")
	case errors.Is(err, chat.ErrRateLimited):
		sess.SendError(id, protocol.ErrTooFast, "کمی آرام‌تر — تعداد پیام‌ها بیش از حد مجاز است")
	case errors.Is(err, chat.ErrForbidden):
		sess.SendError(id, protocol.ErrForbidden, "اجازهٔ این کار را روی این پیام ندارید")
	case errors.Is(err, chat.ErrNotAllowed):
		sess.SendError(id, protocol.ErrForbidden, "اجازهٔ ارسال پیام را ندارید")
	case errors.Is(err, chat.ErrMuted):
		sess.SendError(id, protocol.ErrMuted, "شما میوت شده‌اید و نمی‌توانید پیام بفرستید")
	case errors.Is(err, chat.ErrStickersDisabled):
		sess.SendError(id, protocol.ErrStickersDisabled, "ارسال استیکر در این سرور غیرفعال است")
	case errors.Is(err, chat.ErrNotFound):
		sess.SendError(id, protocol.ErrMessageNotFound, "این پیام دیگر در حافظهٔ روم نیست")
	default:
		var invalid *chat.ValidationError
		if errors.As(err, &invalid) {
			sess.SendError(id, protocol.ErrMessageInvalid, invalid.Msg)
			return
		}
		sess.SendError(id, protocol.ErrInternal, "خطای داخلی سرور")
	}
}
