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
	"tamizchat/internal/bots"
	"tamizchat/internal/chat"
	"tamizchat/internal/config"
	"tamizchat/internal/files"
	"tamizchat/internal/guard"
	"tamizchat/internal/httpapi"
	"tamizchat/internal/media"
	"tamizchat/internal/paint"
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
	files    *files.Manager
	media    *media.Manager
	paint    *paint.Manager
	bots     *bots.Manager
	access   *access.Manager
	guard    *guard.Guard
	proxies  *httpapi.TrustedProxies
	users    Users
	policy   authz.Policy
	serverID string
}

// New builds a gateway. The access manager is both the role store and the
// permission policy, so there is a single source of truth for who may do what.
func New(cfg *config.Config, sessions *session.Manager, roomMgr *rooms.Manager,
	chatMgr *chat.Manager, fileMgr *files.Manager, mediaMgr *media.Manager,
	paintMgr *paint.Manager, botMgr *bots.Manager, accessMgr *access.Manager,
	entryGuard *guard.Guard, proxies *httpapi.TrustedProxies, users Users,
	serverUUID string) *Gateway {
	return &Gateway{
		cfg:      cfg,
		sessions: sessions,
		rooms:    roomMgr,
		chat:     chatMgr,
		files:    fileMgr,
		media:    mediaMgr,
		paint:    paintMgr,
		bots:     botMgr,
		access:   accessMgr,
		guard:    entryGuard,
		proxies:  proxies,
		users:    users,
		policy:   accessMgr,
		serverID: serverUUID,
	}
}

// ServeHTTP upgrades the request and runs the connection until it ends.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The address is checked before the upgrade, so a flood costs the server a
	// rejected HTTP request rather than a WebSocket connection.
	remote := g.remoteAddr(r)
	release, err := g.guard.Admit(remote)
	if err != nil {
		slog.Debug("connection refused by the guard", "remote", remote, "err", err)
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	defer release()

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
	sess, err := g.handshake(ctx, conn, remote)
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
		return nil, g.reject(conn, env.ID, protocol.ErrBadRequest, "the message format is not valid")
	}
	if env.Type != protocol.TypeHello {
		return nil, g.reject(conn, env.ID, protocol.ErrHandshake, "the first message must be hello")
	}

	var hello protocol.Hello
	if err := json.Unmarshal(env.Data, &hello); err != nil {
		return nil, g.reject(conn, env.ID, protocol.ErrBadRequest, "the hello payload is not valid")
	}
	if hello.Protocol != 0 && hello.Protocol != protocol.Version {
		return nil, g.reject(conn, env.ID, protocol.ErrProtocol,
			"the client's protocol version is not compatible with the server")
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
		message := "you are banned from this server"
		if ban.Reason != "" {
			message += ": " + ban.Reason
		}
		return nil, g.reject(conn, env.ID, protocol.ErrBanned, message)
	}

	if want := g.cfg.String(config.KeyServerPassword); want != "" {
		if subtle.ConstantTimeCompare([]byte(want), []byte(hello.Password)) != 1 {
			return nil, g.reject(conn, env.ID, protocol.ErrBadPassword, "the server password is wrong")
		}
	}

	if g.sessions.UsernameTaken(username, clientUUID) {
		return nil, g.reject(conn, env.ID, protocol.ErrUsernameTaken,
			"that username is currently in use on the server")
	}

	sess := session.New(storage.NewUUID(), clientUUID, username, remote)
	g.applyRoles(sess)
	replaced, err := g.sessions.Add(sess)
	if errors.Is(err, session.ErrServerFull) {
		return nil, g.reject(conn, env.ID, protocol.ErrServerFull, "the server is full")
	} else if err != nil {
		return nil, g.reject(conn, env.ID, protocol.ErrInternal, "internal server error")
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
		Bots:       g.bots.Views(),
		Limits: protocol.UserLimits{
			UsernameMin:     minLen,
			UsernameMax:     maxLen,
			MaxUsers:        g.cfg.Int(config.KeyServerMaxUsers),
			MessageMax:      g.cfg.Int(config.KeyChatMaxMessageLen),
			HistoryLimit:    g.cfg.Int(config.KeyRoomsHistoryLimit),
			StickersEnabled: g.cfg.Bool(config.KeyChatStickersEnabled),
			MediaEnabled:    g.media.Enabled(),
			PaintEnabled:    g.cfg.Bool(config.KeyPaintEnabled),
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

// remoteAddr is the address to hold responsible for this request, which behind
// a trusted proxy is the client's rather than the proxy's.
func (g *Gateway) remoteAddr(r *http.Request) string {
	if g.proxies != nil {
		return g.proxies.ClientIP(r)
	}
	return r.RemoteAddr
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
