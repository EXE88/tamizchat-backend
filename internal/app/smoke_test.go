package app_test

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"tamizchat/internal/app"
	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
)

// TestServerEndToEnd boots the real server the way `tamizchat run` does and
// drives it over a real socket: the wiring in app.Run is the one thing the
// package-level tests cannot cover.
func TestServerEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "smoke.db")
	addr := freeAddr(t)

	// Seed the configuration the way an admin would from the panel.
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
	if err := store.CreateRoom(ctx, storage.Room{
		ID: storage.NewUUID(), Name: "Lobby", Capacity: 10,
	}); err != nil {
		t.Fatalf("seed room: %v", err)
	}
	store.Close()

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
	})

	waitForListener(t, addr)

	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	frame, err := protocol.Encode(protocol.TypeHello, "h1", protocol.Hello{
		ClientUUID: "33333333-3333-4333-8333-333333333333",
		Username:   "Smoke",
		Protocol:   protocol.Version,
	})
	if err != nil {
		t.Fatalf("encode hello: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}

	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Type != protocol.TypeWelcome {
		t.Fatalf("expected welcome, got %q: %s", env.Type, env.Data)
	}

	var welcome protocol.Welcome
	if err := json.Unmarshal(env.Data, &welcome); err != nil {
		t.Fatalf("decode welcome: %v", err)
	}
	if len(welcome.Rooms) != 1 || welcome.Rooms[0].Name != "Lobby" {
		t.Fatalf("the seeded room should be visible on connect: %+v", welcome.Rooms)
	}
	if welcome.You.Username != "Smoke" {
		t.Fatalf("unexpected identity: %+v", welcome.You)
	}
}

// freeAddr reserves a loopback port and releases it, so the server can bind it.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never started listening on %s", addr)
}
