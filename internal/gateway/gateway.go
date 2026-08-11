// Package gateway is the WebSocket entry point. It owns the socket lifecycle —
// handshake, read pump, write pump, keepalive — and hands validated frames to
// the rest of the server.
package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"tamizchat/internal/access"
	"tamizchat/internal/authz"
	"tamizchat/internal/chat"
	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/rooms"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
)

const (
	// handshakeTimeout is how long a socket may stay silent before sending its
	// hello frame. It keeps half-open connections from piling up.
	handshakeTimeout = 10 * time.Second
	// readLimit caps a single incoming frame. Chat and control frames are tiny;
	// file uploads go over HTTP in phase 6, not through here.
	readLimit = 64 << 10
	// writeTimeout bounds a single frame write to one client.
	writeTimeout = 10 * time.Second
)

// Users is the persistence the gateway needs; storage.Store satisfies it.
type Users interface {
	TouchUser(ctx context.Context, clientUUID, username string) error
	UpdateUsername(ctx context.Context, clientUUID, username string) error
	MarkUserSeen(ctx context.Context, clientUUID string) error
}

// Gateway serves WebSocket connections.
type Gateway struct {
	cfg      *config.Config
	sessions *session.Manager
	rooms    *rooms.Manager
	chat     *chat.Manager
	access   *access.Manager
	users    Users
	policy   authz.Policy
	serverID string
}

// New builds a gateway. The access manager is both the role store and the
// permission policy, so there is a single source of truth for who may do what.
func New(cfg *config.Config, sessions *session.Manager, roomMgr *rooms.Manager,
	chatMgr *chat.Manager, accessMgr *access.Manager, users Users, serverUUID string) *Gateway {
	return &Gateway{
		cfg:      cfg,
		sessions: sessions,
		rooms:    roomMgr,
		chat:     chatMgr,
		access:   accessMgr,
		users:    users,
		policy:   accessMgr,
		serverID: serverUUID,
	}
}

// ServeHTTP upgrades the request and runs the connection until it ends.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The desktop client is not a browser, so there is no meaningful Origin
		// to check and no cookie-based authority to protect against CSRF.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		slog.Debug("websocket accept failed", "err", err, "remote", r.RemoteAddr)
		return
	}
	conn.SetReadLimit(readLimit)

	ctx := r.Context()
	sess, err := g.handshake(ctx, conn, r.RemoteAddr)
	if err != nil {
		// handshake already reported the reason to the client.
		slog.Debug("handshake rejected", "err", err, "remote", r.RemoteAddr)
		return
	}

	g.serve(ctx, conn, sess)
}

// handshake reads the hello frame, validates it and registers the session.
func (g *Gateway) handshake(ctx context.Context, conn *websocket.Conn, remote string) (*session.Session, error) {
	hsCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	_, data, err := conn.Read(hsCtx)
	if err != nil {
		closeWith(conn, websocket.StatusPolicyViolation, protocol.ErrTimeout)
		return nil, err
	}

	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, g.reject(conn, env.ID, protocol.ErrBadRequest, "قالب پیام معتبر نیست")
	}
	if env.Type != protocol.TypeHello {
		return nil, g.reject(conn, env.ID, protocol.ErrHandshake, "اولین پیام باید hello باشد")
	}

	var hello protocol.Hello
	if err := json.Unmarshal(env.Data, &hello); err != nil {
		return nil, g.reject(conn, env.ID, protocol.ErrBadRequest, "محتوای hello معتبر نیست")
	}
	if hello.Protocol != 0 && hello.Protocol != protocol.Version {
		return nil, g.reject(conn, env.ID, protocol.ErrProtocol,
			"نسخهٔ پروتکل کلاینت با سرور سازگار نیست")
	}

	clientUUID, err := session.NormalizeClientUUID(hello.ClientUUID)
	if err != nil {
		return nil, g.reject(conn, env.ID, protocol.ErrInvalidUUID, err.Error())
	}

	minLen, maxLen := g.cfg.UsernameLimits()
	username, err := session.NormalizeUsername(hello.Username, minLen, maxLen)
	if err != nil {
		return nil, g.reject(conn, env.ID, protocol.ErrInvalidUsername, err.Error())
	}

	if ban, banned := g.access.BanOf(clientUUID); banned {
		message := "شما از این سرور بن شده‌اید"
		if ban.Reason != "" {
			message += ": " + ban.Reason
		}
		return nil, g.reject(conn, env.ID, protocol.ErrBanned, message)
	}

	if want := g.cfg.String(config.KeyServerPassword); want != "" {
		if subtle.ConstantTimeCompare([]byte(want), []byte(hello.Password)) != 1 {
			return nil, g.reject(conn, env.ID, protocol.ErrBadPassword, "رمز سرور نادرست است")
		}
	}

	if g.sessions.UsernameTaken(username, clientUUID) {
		return nil, g.reject(conn, env.ID, protocol.ErrUsernameTaken,
			"این نام کاربری همین حالا در سرور استفاده می‌شود")
	}

	sess := session.New(storage.NewUUID(), clientUUID, username, remote)
	g.applyRoles(sess)
	replaced, err := g.sessions.Add(sess)
	if errors.Is(err, session.ErrServerFull) {
		return nil, g.reject(conn, env.ID, protocol.ErrServerFull, "ظرفیت سرور تکمیل است")
	} else if err != nil {
		return nil, g.reject(conn, env.ID, protocol.ErrInternal, "خطای داخلی سرور")
	}

	if err := g.users.TouchUser(ctx, clientUUID, username); err != nil {
		slog.Error("persist user failed", "client_uuid", clientUUID, "err", err)
	}

	// A reconnecting client keeps the room it was in, so a network blip does
	// not silently drop it out of the conversation.
	if replaced != nil {
		g.rooms.TransferMembership(replaced, sess)
	}

	welcome := protocol.Welcome{
		SessionID:  sess.ID,
		Protocol:   protocol.Version,
		ServerUUID: g.serverID,
		ServerName: g.cfg.String(config.KeyServerName),
		Message:    g.cfg.String(config.KeyServerWelcome),
		Heartbeat:  g.cfg.Int(config.KeyHeartbeatSec),
		You:        sess.User(),
		Users:      g.sessions.Users(),
		Rooms:      g.rooms.Views(),
		Roles:      g.roleViews(),
		Limits: protocol.UserLimits{
			UsernameMin:     minLen,
			UsernameMax:     maxLen,
			MaxUsers:        g.cfg.Int(config.KeyServerMaxUsers),
			MessageMax:      g.cfg.Int(config.KeyChatMaxMessageLen),
			HistoryLimit:    g.cfg.Int(config.KeyRoomsHistoryLimit),
			StickersEnabled: g.cfg.Bool(config.KeyChatStickersEnabled),
		},
		Permissions: authz.Keys(g.permissionsOf(clientUUID)),
	}
	if err := sess.SendMessage(protocol.TypeWelcome, env.ID, welcome); err != nil {
		g.sessions.Remove(sess)
		return nil, err
	}

	// A replaced connection is the same person reconnecting, so the room does
	// not need to hear about a join at all.
	if replaced == nil {
		g.sessions.Broadcast(protocol.TypeUserJoined, sess.User(), sess.ClientUUID)
		slog.Info("user joined", "username", username, "client_uuid", clientUUID,
			"online", g.sessions.Count(), "remote", remote)
	} else {
		slog.Info("session replaced", "username", username, "client_uuid", clientUUID)
	}

	return sess, nil
}

// reject reports a handshake failure to the client and closes the socket.
func (g *Gateway) reject(conn *websocket.Conn, id, code, message string) error {
	if frame, err := protocol.Encode(protocol.TypeError, id,
		protocol.Error{Code: code, Message: message}); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		_ = conn.Write(ctx, websocket.MessageText, frame)
		cancel()
	}
	closeWith(conn, websocket.StatusPolicyViolation, code)
	return errors.New(code)
}

func closeWith(conn *websocket.Conn, status websocket.StatusCode, reason string) {
	// The close reason is capped at 123 bytes by the protocol; our codes are
	// short ASCII identifiers, so they always fit.
	_ = conn.Close(status, reason)
}

// shutSocket ends a connection the server decided to end — a kick, a ban, a
// shutdown — and does not wait for the peer to agree.
//
// The polite close handshake blocks until the client answers with its own close
// frame, and a client that was just kicked has every reason to stop reading. A
// session left waiting on that would stay "online" for seconds after being told
// to go, and would still hold its room slot. The reason has already been
// delivered as an ordinary frame (user.kicked, user.banned, an error), so the
// close frame carries no information the client has not already received.
func shutSocket(conn *websocket.Conn) {
	conn.CloseNow()
}
