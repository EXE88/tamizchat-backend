package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"tamizchat/internal/access"
	"tamizchat/internal/authz"
	"tamizchat/internal/bots"
	"tamizchat/internal/chat"
	"tamizchat/internal/config"
	"tamizchat/internal/files"
	"tamizchat/internal/gateway"
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
	uuidA = "11111111-1111-4111-8111-111111111111"
	uuidB = "22222222-2222-4222-8222-222222222222"
	uuidC = "33333333-3333-4333-8333-333333333333"
)

// TestMain silences the server logs: a failing assertion is easier to find
// without hundreds of migration and presence lines around it.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type fixture struct {
	srv      *httptest.Server
	httpURL  string
	cfg      *config.Config
	store    *storage.Store
	sessions *session.Manager
	rooms    *rooms.Manager
	access   *access.Manager
	files    *files.Manager
	paint    *paint.Manager
	bots     *bots.Manager
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cfg, err := config.Load(ctx, store)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Tests send messages back to back far faster than a person would. Rate
	// limiting is verified deliberately in its own tests, which lower these.
	for key, value := range map[string]string{
		config.KeyChatRateBurst:     "100",
		config.KeyChatRatePerMinute: "600",
	} {
		if err := cfg.Set(ctx, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	accessMgr, err := access.New(ctx, store)
	if err != nil {
		t.Fatalf("access manager: %v", err)
	}

	sessions := session.NewManager(func() int { return cfg.Int(config.KeyServerMaxUsers) })
	roomMgr, err := rooms.NewManager(ctx, store, cfg, sessions, accessMgr)
	if err != nil {
		t.Fatalf("room manager: %v", err)
	}

	if err := cfg.Set(ctx, config.KeyUploadsDir, filepath.Join(t.TempDir(), "uploads")); err != nil {
		t.Fatalf("set upload dir: %v", err)
	}
	// Creating a bot makes it a folder; without this it would land in the
	// package directory instead of somewhere the test cleans up.
	if err := cfg.Set(ctx, config.KeyBotsDir, filepath.Join(t.TempDir(), "bots")); err != nil {
		t.Fatalf("set bots dir: %v", err)
	}

	chatMgr := chat.NewManager(cfg, roomMgr, accessMgr, accessMgr)
	fileMgr, err := files.New(cfg, roomMgr, sessions, accessMgr)
	if err != nil {
		t.Fatalf("file manager: %v", err)
	}

	mediaMgr := media.New(cfg, accessMgr, accessMgr)
	roomMgr.OnMemberLeft(mediaMgr.Disconnect)
	roomMgr.OnDelete(mediaMgr.CloseRoom)

	paintMgr := paint.NewManager(cfg, roomMgr, accessMgr, accessMgr)

	botMgr, err := bots.New(ctx, cfg, store, mediaMgr, roomMgr, sessions)
	if err != nil {
		t.Fatalf("bot manager: %v", err)
	}
	roomMgr.OnDelete(botMgr.RoomGone)

	proxies, err := httpapi.ParseTrustedProxies("")
	if err != nil {
		t.Fatalf("trusted proxies: %v", err)
	}
	entryGuard := guard.New(func() guard.Limits {
		return guard.Limits{
			MaxPerIP:  cfg.Int(config.KeyMaxConnsPerIP),
			Burst:     cfg.Int(config.KeyHandshakeBurst),
			PerMinute: cfg.Int(config.KeyHandshakePerMinute),
		}
	})

	gw := gateway.New(cfg, sessions, roomMgr, chatMgr, fileMgr, mediaMgr,
		paintMgr, botMgr, accessMgr, entryGuard, proxies, store, "server-uuid")

	// The tests drive the real HTTP surface, so the upload and download routes
	// are exercised exactly as a client would reach them.
	srv := httptest.NewServer(httpapi.Handler(httpapi.Deps{
		Config:      cfg,
		ServerUUID:  "server-uuid",
		StartedAt:   time.Now(),
		OnlineUsers: sessions.Count,
		Gateway:     gw,
		Files:       fileMgr,
		MaxUploadBytes: func() int64 {
			return int64(cfg.Int(config.KeyUploadsMaxSizeMB)) << 20
		},
		OnUpload: gw.AnnounceUpload,
		Bots:     botMgr,
		Webhooks: webhookVerifier{media: mediaMgr, bots: botMgr},
	}))
	t.Cleanup(srv.Close)

	f := &fixture{srv: srv, httpURL: srv.URL, cfg: cfg, store: store, sessions: sessions,
		rooms: roomMgr, access: accessMgr, files: fileMgr, paint: paintMgr, bots: botMgr}

	// uuidA is the fixture's administrator. Most tests need someone who can
	// create rooms; the tests about permissions use the other identities,
	// which hold only the default role.
	f.makeAdmin(t, uuidA)
	return f
}

// webhookVerifier mirrors the wiring app.Run does, so the tests exercise the
// same path a real LiveKit callback takes.
type webhookVerifier struct {
	media *media.Manager
	bots  *bots.Manager
}

func (w webhookVerifier) HandleWebhook(ctx context.Context, authHeader string, body []byte) error {
	event, err := w.media.VerifyWebhook(authHeader, body)
	if err != nil {
		return err
	}
	if event.Event == media.EventIngressEnded {
		w.bots.TrackEnded(ctx, event.Ingress.IngressID)
	}
	return nil
}

// defaultRolePermissionsWithout removes one permission from the role everybody
// holds, which is how a server operator restricts what ordinary users may do.
func (f *fixture) defaultRolePermissionsWithout(t *testing.T, key string) {
	t.Helper()
	drop, ok := authz.Lookup(key)
	if !ok {
		t.Fatalf("unknown permission %q", key)
	}

	role, err := f.store.GetRole(context.Background(), storage.RoleIDDefault)
	if err != nil {
		t.Fatalf("get default role: %v", err)
	}

	perms := authz.Permission(role.Permissions) &^ drop.Perm
	if _, err := f.access.UpdateRole(context.Background(), storage.RoleIDDefault,
		access.RoleSpec{Permissions: &perms}); err != nil {
		t.Fatalf("update default role: %v", err)
	}
}

// defaultRolePermissionsWith adds one permission to the role everybody holds.
func (f *fixture) defaultRolePermissionsWith(t *testing.T, key string) {
	t.Helper()
	add, ok := authz.Lookup(key)
	if !ok {
		t.Fatalf("unknown permission %q", key)
	}

	role, err := f.store.GetRole(context.Background(), storage.RoleIDDefault)
	if err != nil {
		t.Fatalf("get default role: %v", err)
	}

	perms := authz.Permission(role.Permissions) | add.Perm
	if _, err := f.access.UpdateRole(context.Background(), storage.RoleIDDefault,
		access.RoleSpec{Permissions: &perms}); err != nil {
		t.Fatalf("update default role: %v", err)
	}
}

// makeAdmin grants the built-in admin role, the way an operator would from
// the panel.
func (f *fixture) makeAdmin(t *testing.T, clientUUID string) {
	t.Helper()
	if err := f.access.Grant(context.Background(), "", clientUUID, storage.RoleIDAdmin); err != nil {
		t.Fatalf("grant admin role: %v", err)
	}
}

type client struct {
	t    *testing.T
	conn *websocket.Conn
}

func (f *fixture) dial(t *testing.T) *client {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), f.srv.URL+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return &client{t: t, conn: conn}
}

func (c *client) send(typ, id string, payload any) {
	c.t.Helper()
	frame, err := protocol.Encode(typ, id, payload)
	if err != nil {
		c.t.Fatalf("encode %s: %v", typ, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, frame); err != nil {
		c.t.Fatalf("write %s: %v", typ, err)
	}
}

func (c *client) recv() protocol.Envelope {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		c.t.Fatalf("decode frame %q: %v", data, err)
	}
	return env
}

// expect reads one frame and asserts its type.
func (c *client) expect(typ string) protocol.Envelope {
	c.t.Helper()
	env := c.recv()
	if env.Type != typ {
		c.t.Fatalf("expected frame %q, got %q (%s)", typ, env.Type, env.Data)
	}
	return env
}

func (c *client) decode(env protocol.Envelope, into any) {
	c.t.Helper()
	if err := json.Unmarshal(env.Data, into); err != nil {
		c.t.Fatalf("decode payload: %v", err)
	}
}

// hello performs a handshake and returns the welcome payload.
func (f *fixture) hello(t *testing.T, uuid, username, password string) (*client, protocol.Welcome) {
	t.Helper()
	c := f.dial(t)
	c.send(protocol.TypeHello, "h1", protocol.Hello{
		ClientUUID: uuid, Username: username, Password: password, Protocol: protocol.Version,
	})
	env := c.expect(protocol.TypeWelcome)
	if env.ID != "h1" {
		t.Fatalf("welcome should echo the request id, got %q", env.ID)
	}
	var w protocol.Welcome
	c.decode(env, &w)
	return c, w
}

func TestHandshakeStoresUserAndAnnouncesPresence(t *testing.T) {
	f := newFixture(t)

	alice, welcome := f.hello(t, uuidA, "Alice", "")
	if welcome.You.Username != "Alice" || welcome.You.ClientUUID != uuidA {
		t.Fatalf("unexpected identity in welcome: %+v", welcome.You)
	}
	if len(welcome.Users) != 1 {
		t.Fatalf("expected to see myself in the user list, got %d", len(welcome.Users))
	}
	if welcome.ServerUUID != "server-uuid" || welcome.Protocol != protocol.Version {
		t.Fatalf("unexpected server details: %+v", welcome)
	}

	// The second client should appear in nobody's welcome but alice's feed.
	_, welcomeB := f.hello(t, uuidB, "Bob", "")
	if len(welcomeB.Users) != 2 {
		t.Fatalf("Bob should see both users, got %d", len(welcomeB.Users))
	}

	joined := alice.expect(protocol.TypeUserJoined)
	var user protocol.User
	alice.decode(joined, &user)
	if user.Username != "Bob" {
		t.Fatalf("expected Bob's join, got %+v", user)
	}

	users, err := f.store.RecentUsers(context.Background(), 10)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("both clients should be persisted, got %d", len(users))
	}
}

func TestFirstFrameMustBeHello(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t)
	c.send(protocol.TypePing, "p1", nil)

	env := c.expect(protocol.TypeError)
	var e protocol.Error
	c.decode(env, &e)
	if e.Code != protocol.ErrHandshake {
		t.Fatalf("expected %s, got %s", protocol.ErrHandshake, e.Code)
	}
}

func TestPasswordIsEnforced(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyServerPassword, "s3cret"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	c := f.dial(t)
	c.send(protocol.TypeHello, "", protocol.Hello{ClientUUID: uuidA, Username: "Alice", Password: "wrong"})
	var e protocol.Error
	c.decode(c.expect(protocol.TypeError), &e)
	if e.Code != protocol.ErrBadPassword {
		t.Fatalf("expected %s, got %s", protocol.ErrBadPassword, e.Code)
	}

	if _, w := f.hello(t, uuidA, "Alice", "s3cret"); w.You.Username != "Alice" {
		t.Fatalf("correct password should be accepted")
	}
}

func TestRejectsBadIdentity(t *testing.T) {
	cases := []struct {
		name     string
		hello    protocol.Hello
		wantCode string
	}{
		{"malformed uuid", protocol.Hello{ClientUUID: "not-a-uuid", Username: "Alice"}, protocol.ErrInvalidUUID},
		{"short username", protocol.Hello{ClientUUID: uuidA, Username: "a"}, protocol.ErrInvalidUsername},
		{"blank username", protocol.Hello{ClientUUID: uuidA, Username: "   "}, protocol.ErrInvalidUsername},
		{"future protocol", protocol.Hello{ClientUUID: uuidA, Username: "Alice", Protocol: 99}, protocol.ErrProtocol},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			c := f.dial(t)
			c.send(protocol.TypeHello, "", tc.hello)
			var e protocol.Error
			c.decode(c.expect(protocol.TypeError), &e)
			if e.Code != tc.wantCode {
				t.Fatalf("expected %s, got %s (%s)", tc.wantCode, e.Code, e.Message)
			}
		})
	}
}

func TestUsernameCollisionIsRejectedCaseInsensitively(t *testing.T) {
	f := newFixture(t)
	f.hello(t, uuidA, "Alice", "")

	c := f.dial(t)
	c.send(protocol.TypeHello, "", protocol.Hello{ClientUUID: uuidB, Username: "alice"})
	var e protocol.Error
	c.decode(c.expect(protocol.TypeError), &e)
	if e.Code != protocol.ErrUsernameTaken {
		t.Fatalf("expected %s, got %s", protocol.ErrUsernameTaken, e.Code)
	}
}

func TestServerFullIsRejected(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyServerMaxUsers, "1"); err != nil {
		t.Fatalf("set max users: %v", err)
	}
	f.hello(t, uuidA, "Alice", "")

	c := f.dial(t)
	c.send(protocol.TypeHello, "", protocol.Hello{ClientUUID: uuidB, Username: "Bob"})
	var e protocol.Error
	c.decode(c.expect(protocol.TypeError), &e)
	if e.Code != protocol.ErrServerFull {
		t.Fatalf("expected %s, got %s", protocol.ErrServerFull, e.Code)
	}
}

// A client that reconnects before the server noticed the dead socket must take
// over its own identity instead of being refused as a duplicate — and the other
// users should not see a leave/join flicker.
func TestReconnectWithSameUUIDReplacesOldSession(t *testing.T) {
	f := newFixture(t)
	observer, _ := f.hello(t, uuidB, "Observer", "")

	f.hello(t, uuidA, "Alice", "")
	observer.expect(protocol.TypeUserJoined) // Alice's first connection

	fresh, welcome := f.hello(t, uuidA, "Alice", "")
	if welcome.You.ClientUUID != uuidA {
		t.Fatalf("unexpected identity: %+v", welcome.You)
	}
	if got := f.sessions.Count(); got != 2 {
		t.Fatalf("expected 2 online sessions after takeover, got %d", got)
	}

	// The takeover must be silent for everyone else: the next thing the
	// observer hears is Alice's rename, not a join or a leave.
	fresh.send(protocol.TypeRename, "r1", protocol.Rename{Username: "Alice2"})
	fresh.expect(protocol.TypeUserUpdated)

	env := observer.expect(protocol.TypeUserUpdated)
	var u protocol.User
	observer.decode(env, &u)
	if u.Username != "Alice2" {
		t.Fatalf("observer got unexpected update: %+v", u)
	}
}

func TestRenameIsValidatedAndBroadcast(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeRename, "r1", protocol.Rename{Username: "Bob"})
	var e protocol.Error
	alice.decode(alice.expect(protocol.TypeError), &e)
	if e.Code != protocol.ErrUsernameTaken {
		t.Fatalf("expected %s, got %s", protocol.ErrUsernameTaken, e.Code)
	}

	alice.send(protocol.TypeRename, "r2", protocol.Rename{Username: "  Ali   Reza "})
	var me protocol.User
	alice.decode(alice.expect(protocol.TypeUserUpdated), &me)
	if me.Username != "Ali Reza" {
		t.Fatalf("whitespace should be normalized, got %q", me.Username)
	}

	var seen protocol.User
	bob.decode(bob.expect(protocol.TypeUserUpdated), &seen)
	if seen.Username != "Ali Reza" {
		t.Fatalf("Bob should see the rename, got %q", seen.Username)
	}

	users, err := f.store.RecentUsers(context.Background(), 10)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	for _, u := range users {
		if u.ClientUUID == uuidA && u.Username != "Ali Reza" {
			t.Fatalf("rename should be persisted, stored %q", u.Username)
		}
	}
}

func TestPingAnswersPong(t *testing.T) {
	f := newFixture(t)
	c, _ := f.hello(t, uuidA, "Alice", "")

	c.send(protocol.TypePing, "ping-1", nil)
	env := c.expect(protocol.TypePong)
	if env.ID != "ping-1" {
		t.Fatalf("pong should echo the id, got %q", env.ID)
	}
}

func TestUnknownFrameIsReportedWithoutClosing(t *testing.T) {
	f := newFixture(t)
	c, _ := f.hello(t, uuidA, "Alice", "")

	c.send("does.not.exist", "x1", nil)
	var e protocol.Error
	c.decode(c.expect(protocol.TypeError), &e)
	if e.Code != protocol.ErrBadRequest {
		t.Fatalf("expected %s, got %s", protocol.ErrBadRequest, e.Code)
	}

	// The session must survive a bad frame.
	c.send(protocol.TypePing, "still-here", nil)
	c.expect(protocol.TypePong)
}

func TestLeaveIsBroadcastAndPersisted(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	bob.conn.Close(websocket.StatusNormalClosure, "bye")

	env := alice.expect(protocol.TypeUserLeft)
	var left protocol.UserLeft
	alice.decode(env, &left)
	if left.ClientUUID != uuidB || left.Username != "Bob" {
		t.Fatalf("unexpected leave payload: %+v", left)
	}

	deadline := time.Now().Add(2 * time.Second)
	for f.sessions.Count() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("session was not released, still %d online", f.sessions.Count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
