package app_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"tamizchat/internal/app"
	"tamizchat/internal/config"
	"tamizchat/internal/control"
	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
)

// liveServer boots the real server and returns a control client for it, which
// is exactly what the admin panel holds.
type liveServer struct {
	dbPath  string
	addr    string
	control *control.Client
	store   *storage.Store
	cfg     *config.Config
}

func startServer(t *testing.T) *liveServer {
	t.Helper()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "live.db")
	addr := freeAddr(t)

	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg, err := config.Load(ctx, store)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.Set(ctx, config.KeyListenAddr, addr); err != nil {
		t.Fatalf("set addr: %v", err)
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx, app.Options{DBPath: dbPath}) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server exited with error: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("server did not shut down")
		}
		store.Close()
	})

	waitForListener(t, addr)

	return &liveServer{
		dbPath:  dbPath,
		addr:    addr,
		control: control.NewClient(dbPath),
		store:   store,
		cfg:     cfg,
	}
}

// connect joins the server as a client and returns the open socket.
func (s *liveServer) connect(t *testing.T, uuid, username string) *websocket.Conn {
	t.Helper()
	ctx := context.Background()

	conn, _, err := websocket.Dial(ctx, "ws://"+s.addr+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })

	send(t, ctx, conn, protocol.TypeHello, "h1", protocol.Hello{
		ClientUUID: uuid, Username: username, Protocol: protocol.Version,
	})
	expect(t, ctx, conn, protocol.TypeWelcome, nil)
	return conn
}

func TestControlReportsStatusAndOnlineUsers(t *testing.T) {
	s := startServer(t)

	if _, running := s.control.Running(); !running {
		t.Fatal("the panel should see the server as running")
	}

	status, err := s.control.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.PID != os.Getpid() || status.ListenAddr != s.addr {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.OnlineUsers != 0 {
		t.Fatalf("nobody is connected yet, got %d", status.OnlineUsers)
	}

	s.connect(t, "11111111-1111-4111-8111-111111111111", "Alice")

	deadline := time.Now().Add(3 * time.Second)
	for {
		users, err := s.control.Online()
		if err != nil {
			t.Fatalf("online: %v", err)
		}
		if len(users) == 1 {
			if users[0].Username != "Alice" {
				t.Fatalf("unexpected user: %+v", users[0])
			}
			if users[0].Roles == "" {
				t.Fatalf("the panel should see the user's roles: %+v", users[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the connected user never showed up: %+v", users)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// This is the debt every earlier phase carried: a panel edit used to wait for a
// restart. It must now reach the running server.
func TestReloadAppliesPanelChangesToTheRunningServer(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()

	// The panel writes straight to the database, as it always has.
	if err := s.cfg.Set(ctx, config.KeyServerName, "New name"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	roomID := storage.NewUUID()
	if err := s.store.CreateRoom(ctx, storage.Room{
		ID: roomID, Name: "New room", Capacity: 10,
	}); err != nil {
		t.Fatalf("create room: %v", err)
	}

	// Before the reload, the running server knows nothing about either.
	before, err := s.control.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if before.Rooms != 0 || before.ServerName == "New name" {
		t.Fatalf("the running server should not have picked these up yet: %+v", before)
	}

	result, err := s.control.Reload()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if result.Settings != 1 {
		t.Fatalf("exactly one setting changed, got %d", result.Settings)
	}
	if result.Rooms != 1 {
		t.Fatalf("the new room should be loaded, got %d", result.Rooms)
	}

	after, err := s.control.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if after.ServerName != "New name" || after.Rooms != 1 {
		t.Fatalf("the reload did not take effect: %+v", after)
	}

	// And a client connecting now sees the room without anything restarting.
	conn := s.connect(t, "22222222-2222-4222-8222-222222222222", "Bob")
	send(t, ctx, conn, protocol.TypeRoomList, "l1", nil)
	var list protocol.RoomList
	expect(t, ctx, conn, protocol.TypeRooms, &list)
	if len(list.Rooms) != 1 || list.Rooms[0].Name != "New room" {
		t.Fatalf("the client should see the new room: %+v", list.Rooms)
	}
}

// A room deleted from the panel must not leave anyone standing in it.
func TestReloadEvictsMembersOfADeletedRoom(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()

	roomID := storage.NewUUID()
	if err := s.store.CreateRoom(ctx, storage.Room{ID: roomID, Name: "Lobby", Capacity: 10}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := s.control.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	conn := s.connect(t, "11111111-1111-4111-8111-111111111111", "Alice")
	send(t, ctx, conn, protocol.TypeRoomJoin, "j1", protocol.RoomJoin{RoomID: roomID})
	expect(t, ctx, conn, protocol.TypeRoomJoined, nil)

	if err := s.store.DeleteRoom(ctx, roomID); err != nil {
		t.Fatalf("delete room: %v", err)
	}
	if _, err := s.control.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	var left protocol.RoomLeft
	expect(t, ctx, conn, protocol.TypeRoomLeft, &left)
	if left.Reason != protocol.ReasonRoomDeleted {
		t.Fatalf("unexpected eject reason: %+v", left)
	}
	expect(t, ctx, conn, protocol.TypeRoomDeleted, nil)
}

// A role granted from the panel has to reach someone who is already connected.
func TestReloadAppliesRolesToConnectedUsers(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()

	const uuid = "11111111-1111-4111-8111-111111111111"
	conn := s.connect(t, uuid, "Alice")

	// Without the admin role, creating a room is refused.
	send(t, ctx, conn, protocol.TypeRoomCreate, "c1", protocol.RoomCreate{Name: "Nope"})
	var failure protocol.Error
	expect(t, ctx, conn, protocol.TypeError, &failure)
	if failure.Code != protocol.ErrForbidden {
		t.Fatalf("expected forbidden, got %+v", failure)
	}

	if err := s.store.GrantRole(ctx, uuid, storage.RoleIDAdmin, ""); err != nil {
		t.Fatalf("grant role: %v", err)
	}
	if _, err := s.control.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	send(t, ctx, conn, protocol.TypeRoomCreate, "c2", protocol.RoomCreate{Name: "Lobby"})
	var room protocol.Room
	expect(t, ctx, conn, protocol.TypeRoomCreated, &room)
	if room.Name != "Lobby" {
		t.Fatalf("unexpected room: %+v", room)
	}
}

func TestControlKickAndNotice(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()

	const uuid = "11111111-1111-4111-8111-111111111111"
	conn := s.connect(t, uuid, "Alice")

	if err := s.control.Notice("the server restarts in ten minutes"); err != nil {
		t.Fatalf("notice: %v", err)
	}
	var notice protocol.ServerNotice
	expect(t, ctx, conn, protocol.TypeServerNotice, &notice)
	if notice.Text == "" || notice.From == "" {
		t.Fatalf("unexpected notice: %+v", notice)
	}

	if err := s.control.Kick(uuid, "for testing"); err != nil {
		t.Fatalf("kick: %v", err)
	}
	var kicked protocol.Moderation
	expect(t, ctx, conn, protocol.TypeUserKicked, &kicked)
	if kicked.ClientUUID != uuid || kicked.Reason != "for testing" {
		t.Fatalf("unexpected kick event: %+v", kicked)
	}

	// Kicking somebody who is not connected is an error, not a silent no-op.
	if err := s.control.Kick("33333333-3333-4333-8333-333333333333", ""); err == nil {
		t.Fatal("kicking an offline user should fail")
	}
}

// The control channel is the keys to the server, so the token is the whole
// authentication and a wrong one must get nothing.
func TestControlRefusesTheWrongToken(t *testing.T) {
	s := startServer(t)

	raw, err := os.ReadFile(control.EndpointPath(s.dbPath))
	if err != nil {
		t.Fatalf("read endpoint: %v", err)
	}
	var endpoint struct {
		Addr  string `json:"addr"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &endpoint); err != nil {
		t.Fatalf("decode endpoint: %v", err)
	}
	if endpoint.Token == "" {
		t.Fatal("the endpoint file must carry a token")
	}

	conn, err := net.DialTimeout("tcp", endpoint.Addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if err := json.NewEncoder(conn).Encode(control.Request{
		Token: "guessed", Command: control.CmdStatus,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	var resp control.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.OK {
		t.Fatal("a wrong token must be refused")
	}
}

// The endpoint file is only meaningful while the server runs; leaving it behind
// would make the panel think a dead server is alive.
func TestEndpointFileIsRemovedOnShutdown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "live.db")
	addr := freeAddr(t)

	ctx := context.Background()
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg, err := config.Load(ctx, store)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.Set(ctx, config.KeyListenAddr, addr); err != nil {
		t.Fatalf("set addr: %v", err)
	}
	store.Close()

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx, app.Options{DBPath: dbPath}) }()
	waitForListener(t, addr)

	path := control.EndpointPath(dbPath)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the endpoint file should exist while running: %v", err)
	}

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server did not shut down")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the endpoint file should be gone after shutdown, got %v", err)
	}

	// And the panel agrees the server is not running.
	if _, running := control.NewClient(dbPath).Running(); running {
		t.Fatal("the panel should report a stopped server")
	}
}

// A server killed outright never runs its shutdown, so the endpoint file is
// left behind. The panel must notice that nothing answers rather than
// reporting a dead server as alive.
func TestStaleEndpointFileIsNotMistakenForARunningServer(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gone.db")

	// An address nothing is listening on, written as a real server would.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	stale, _ := json.Marshal(map[string]any{"addr": addr, "token": "leftover", "pid": 4242})
	if err := os.WriteFile(control.EndpointPath(dbPath), stale, 0o600); err != nil {
		t.Fatalf("write stale endpoint: %v", err)
	}

	client := control.NewClient(dbPath)
	if _, running := client.Running(); running {
		t.Fatal("a stale endpoint file must not look like a running server")
	}
	if _, err := client.Status(); err == nil {
		t.Fatal("commands against a stale endpoint should fail")
	}
}

func TestPanelWorksWithoutARunningServer(t *testing.T) {
	client := control.NewClient(filepath.Join(t.TempDir(), "nothing.db"))

	if _, running := client.Running(); running {
		t.Fatal("nothing is running here")
	}
	if _, err := client.Status(); err == nil {
		t.Fatal("status should fail without a server")
	}
	if _, err := client.Reload(); err == nil {
		t.Fatal("reload should fail without a server")
	}
}
