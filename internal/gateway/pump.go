package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
)

// serve runs the read and write pumps until either side goes away, then tears
// the session down and announces the departure.
func (g *Gateway) serve(ctx context.Context, conn *websocket.Conn, sess *session.Session) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go g.writePump(ctx, conn, sess)
	go g.keepalive(ctx, conn, sess)

	g.readPump(ctx, conn, sess)

	sess.Close(protocol.ReasonClientLeft)
	g.leave(sess)
	closeWith(conn, websocket.StatusNormalClosure, sess.Reason())
}

// leave deregisters the session and tells everyone else, unless this session
// was already superseded by a newer connection from the same client.
func (g *Gateway) leave(sess *session.Session) {
	if !g.sessions.Remove(sess) {
		return // replaced: a newer session owns this identity now
	}

	// Take the user out of their room first, so room members hear the exit
	// before the server-wide leave arrives.
	g.rooms.Disconnect(sess)
	g.chat.Forget(sess.ClientUUID)
	g.paint.Forget(sess.ClientUUID)

	reason := sess.Reason()
	g.sessions.Broadcast(protocol.TypeUserLeft, protocol.UserLeft{
		ClientUUID: sess.ClientUUID,
		Username:   sess.Username(),
		Reason:     reason,
	}, sess.ClientUUID)

	// Detached from the request context: the socket is already gone.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.users.MarkUserSeen(ctx, sess.ClientUUID); err != nil {
		slog.Debug("mark user seen failed", "client_uuid", sess.ClientUUID, "err", err)
	}

	slog.Info("user left", "username", sess.Username(), "client_uuid", sess.ClientUUID,
		"reason", reason, "online", g.sessions.Count())
}

// readPump consumes client frames until the socket closes.
func (g *Gateway) readPump(ctx context.Context, conn *websocket.Conn, sess *session.Session) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			sess.SendError("", protocol.ErrBadRequest, "فقط پیام متنی JSON پذیرفته می‌شود")
			continue
		}
		if sess.Closed() {
			return
		}
		g.dispatch(ctx, sess, data)
	}
}

// dispatch routes one decoded frame to its handler.
func (g *Gateway) dispatch(ctx context.Context, sess *session.Session, data []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		sess.SendError("", protocol.ErrBadRequest, "قالب پیام معتبر نیست")
		return
	}

	switch env.Type {
	case protocol.TypePing:
		_ = sess.SendMessage(protocol.TypePong, env.ID, nil)

	case protocol.TypeRename:
		g.handleRename(ctx, sess, env)

	case protocol.TypeRoomList:
		g.handleRoomList(sess, env)
	case protocol.TypeRoomJoin:
		g.handleRoomJoin(sess, env)
	case protocol.TypeRoomLeave:
		g.handleRoomLeave(sess, env)
	case protocol.TypeRoomCreate:
		g.handleRoomCreate(ctx, sess, env)
	case protocol.TypeRoomUpdate:
		g.handleRoomUpdate(ctx, sess, env)
	case protocol.TypeRoomDelete:
		g.handleRoomDelete(ctx, sess, env)

	case protocol.TypeChatSend:
		g.handleChatSend(sess, env)
	case protocol.TypeChatHistory:
		g.handleChatHistory(sess, env)
	case protocol.TypeChatEdit:
		g.handleChatEdit(sess, env)
	case protocol.TypeChatDelete:
		g.handleChatDelete(sess, env)
	case protocol.TypeChatTyping:
		g.handleChatTyping(sess, env)

	case protocol.TypeAdminKick:
		g.handleAdminKick(ctx, sess, env)
	case protocol.TypeAdminBan:
		g.handleAdminBan(ctx, sess, env)
	case protocol.TypeAdminUnban:
		g.handleAdminUnban(ctx, sess, env)
	case protocol.TypeAdminMute:
		g.handleAdminMute(ctx, sess, env)
	case protocol.TypeAdminUnmute:
		g.handleAdminUnmute(ctx, sess, env)
	case protocol.TypeAdminMove:
		g.handleAdminMove(ctx, sess, env)
	case protocol.TypeAdminSanctions:
		g.handleAdminSanctions(sess, env)
	case protocol.TypeAdminRoleList:
		g.handleRoleList(sess, env)
	case protocol.TypeAdminRoleCreate:
		g.handleRoleCreate(ctx, sess, env)
	case protocol.TypeAdminRoleUpdate:
		g.handleRoleUpdate(ctx, sess, env)
	case protocol.TypeAdminRoleDelete:
		g.handleRoleDelete(ctx, sess, env)
	case protocol.TypeAdminRoleGrant:
		g.handleRoleGrant(ctx, sess, env)
	case protocol.TypeAdminRoleRevoke:
		g.handleRoleRevoke(ctx, sess, env)

	case protocol.TypeFileUploadRequest:
		g.handleFileUploadRequest(sess, env)
	case protocol.TypeFileDownloadToken:
		g.handleFileDownloadToken(sess, env)

	case protocol.TypeMediaToken:
		g.handleMediaToken(sess, env)
	case protocol.TypeMediaSetState:
		g.handleMediaSetState(sess, env)

	case protocol.TypePaintBegin:
		g.handlePaintBegin(sess, env)
	case protocol.TypePaintAppend:
		g.handlePaintAppend(sess, env)
	case protocol.TypePaintEnd:
		g.handlePaintEnd(sess, env)
	case protocol.TypePaintUndo:
		g.handlePaintUndo(sess, env)
	case protocol.TypePaintClear:
		g.handlePaintClear(sess, env)
	case protocol.TypePaintState:
		g.handlePaintState(sess, env)

	case protocol.TypeBotList:
		g.handleBotList(sess, env)
	case protocol.TypeBotControl:
		g.handleBotControl(ctx, sess, env)
	case protocol.TypeBotMove:
		g.handleBotMove(ctx, sess, env)

	case protocol.TypeHello:
		sess.SendError(env.ID, protocol.ErrBadRequest, "hello فقط یک‌بار در ابتدای اتصال پذیرفته می‌شود")

	default:
		sess.SendError(env.ID, protocol.ErrBadRequest, "نوع پیام پشتیبانی نمی‌شود: "+env.Type)
	}
}

func (g *Gateway) handleRename(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	var req protocol.Rename
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	minLen, maxLen := g.cfg.UsernameLimits()
	name, err := session.NormalizeUsername(req.Username, minLen, maxLen)
	if err != nil {
		sess.SendError(env.ID, protocol.ErrInvalidUsername, err.Error())
		return
	}
	if name == sess.Username() {
		_ = sess.SendMessage(protocol.TypeUserUpdated, env.ID, sess.User())
		return
	}
	if g.sessions.UsernameTaken(name, sess.ClientUUID) {
		sess.SendError(env.ID, protocol.ErrUsernameTaken, "این نام کاربری استفاده شده است")
		return
	}

	old := sess.Username()
	sess.SetUsername(name)
	if err := g.users.UpdateUsername(ctx, sess.ClientUUID, name); err != nil {
		slog.Error("persist rename failed", "client_uuid", sess.ClientUUID, "err", err)
	}

	_ = sess.SendMessage(protocol.TypeUserUpdated, env.ID, sess.User())
	g.sessions.Broadcast(protocol.TypeUserUpdated, sess.User(), sess.ClientUUID)
	slog.Info("user renamed", "client_uuid", sess.ClientUUID, "from", old, "to", name)
}

// writePump drains the session's outbound queue onto the socket. All writes
// happen here, because a websocket connection allows only one writer at a time.
func (g *Gateway) writePump(ctx context.Context, conn *websocket.Conn, sess *session.Session) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.Done():
			// Deliver what is still queued, then close the socket. Closing
			// here is what unblocks the read pump when the session was ended
			// by the server rather than by the client.
			g.flush(conn, sess)
			shutSocket(conn)
			return
		case frame := <-sess.Outbound():
			if !g.write(ctx, conn, frame) {
				sess.Close(protocol.ReasonClientLeft)
				return
			}
		}
	}
}

// flush makes a best effort to deliver what is already queued — typically the
// error frame explaining why the session is ending.
func (g *Gateway) flush(conn *websocket.Conn, sess *session.Session) {
	for {
		select {
		case frame := <-sess.Outbound():
			if !g.write(context.Background(), conn, frame) {
				return
			}
		default:
			return
		}
	}
}

func (g *Gateway) write(ctx context.Context, conn *websocket.Conn, frame []byte) bool {
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, frame); err != nil {
		slog.Debug("websocket write failed", "err", err)
		return false
	}
	return true
}

// keepalive pings the client on the configured interval. A client that stops
// answering is dropped, which is how the server notices dead connections that
// were never closed cleanly.
func (g *Gateway) keepalive(ctx context.Context, conn *websocket.Conn, sess *session.Session) {
	interval := time.Duration(g.cfg.Int(config.KeyHeartbeatSec)) * time.Second
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, interval)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				slog.Debug("keepalive failed", "client_uuid", sess.ClientUUID, "err", err)
				sess.Close(protocol.ReasonTimeout)
				return
			}
		}
	}
}
