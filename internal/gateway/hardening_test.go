package gateway_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
)

// One machine must not be able to hold open as many sockets as it likes: past
// the handshake every limit is per user, and a client with no identity yet has
// none of those.
func TestConnectionsPerAddressAreCapped(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyMaxConnsPerIP, "2"); err != nil {
		t.Fatalf("set cap: %v", err)
	}

	f.dial(t)
	f.dial(t)

	// The third is refused before the upgrade, so the client sees an HTTP
	// status rather than a WebSocket that closes immediately.
	_, resp, err := websocket.Dial(context.Background(), f.srv.URL+"/ws", nil)
	if err == nil {
		t.Fatal("the third connection should have been refused")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %v", resp)
	}
}

// A client that disconnects and comes back must not be locked out by its own
// previous socket.
func TestClosingAConnectionFreesTheSlot(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyMaxConnsPerIP, "1"); err != nil {
		t.Fatalf("set cap: %v", err)
	}

	first, welcome := f.hello(t, uuidA, "Alice", "")
	if welcome.You.Username != "Alice" {
		t.Fatalf("unexpected identity: %+v", welcome.You)
	}
	first.conn.Close(websocket.StatusNormalClosure, "bye")

	// The server releases the slot when the connection unwinds, which happens
	// a moment after the close frame.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, err := websocket.Dial(context.Background(), f.srv.URL+"/ws", nil)
		if err == nil {
			conn.CloseNow()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the slot was never released")
}

func TestConnectionRateIsLimited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.cfg.Set(ctx, config.KeyMaxConnsPerIP, "0"); err != nil {
		t.Fatalf("set cap: %v", err)
	}
	if err := f.cfg.Set(ctx, config.KeyHandshakeBurst, "3"); err != nil {
		t.Fatalf("set burst: %v", err)
	}
	if err := f.cfg.Set(ctx, config.KeyHandshakePerMinute, "1"); err != nil {
		t.Fatalf("set rate: %v", err)
	}

	for i := 0; i < 3; i++ {
		f.dial(t)
	}

	_, resp, err := websocket.Dial(ctx, f.srv.URL+"/ws", nil)
	if err == nil {
		t.Fatal("the burst is spent, this should have been refused")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %v", resp)
	}
}

// Every response carries the headers that tell a browser to assume nothing:
// none of this is a web app, and an uploaded file must never execute as one.
func TestSecurityHeadersArePresent(t *testing.T) {
	f := newFixture(t)

	resp, err := http.Get(f.httpURL + "/api/v1/server-info")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("unexpected CSP: %q", resp.Header.Get("Content-Security-Policy"))
	}
}

// Malformed and hostile frames must be answered, not crash anything. Each of
// these has broken a server somewhere.
func TestHostileFramesAreSurvived(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	frames := []string{
		`{`,                                     // truncated JSON
		`[]`,                                    // right JSON, wrong shape
		`{"t":"chat.send","d":"not an object"}`, // payload of the wrong type
		`{"t":"chat.send","d":null}`,            // missing payload
		`{"t":"room.join","d":{"room_id":null}}`,
		`{"t":"paint.append","d":{"stroke_id":"","points":null}}`,
		`{"t":"admin.ban","d":{"client_uuid":"","duration_sec":-1}}`,
		`{"t":"bot.control","d":{"bot_id":"x","action":"","track_index":-5}}`,
		`{"t":"` + strings.Repeat("A", 5000) + `"}`, // absurd frame type
	}

	ctx := context.Background()
	for _, frame := range frames {
		if err := alice.conn.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			t.Fatalf("write %.40s: %v", frame, err)
		}
		// Every one of these is answered with an error frame; none of them ends
		// the connection or the process.
		env := alice.recv()
		if env.Type != protocol.TypeError {
			t.Fatalf("frame %.40s got %q instead of an error", frame, env.Type)
		}
	}

	// And the session still works afterwards.
	alice.send(protocol.TypePing, "alive", nil)
	alice.expect(protocol.TypePong)
}

// A frame that is not text at all — a binary blob — must be refused without
// dropping the connection.
func TestBinaryFramesAreRefused(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	ctx := context.Background()
	if err := alice.conn.Write(ctx, websocket.MessageBinary, []byte{0x00, 0x01, 0x02}); err != nil {
		t.Fatalf("write: %v", err)
	}
	alice.expectError(protocol.ErrBadRequest)

	alice.send(protocol.TypePing, "alive", nil)
	alice.expect(protocol.TypePong)
}
