package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/httpapi"
	"tamizchat/internal/storage"
)

func newHandler(t *testing.T, online int) (http.Handler, *config.Config) {
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

	h := httpapi.Handler(httpapi.Deps{
		Config:      cfg,
		ServerUUID:  "server-uuid",
		StartedAt:   time.Now(),
		OnlineUsers: func() int { return online },
		Gateway: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}),
	})
	return h, cfg
}

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	var body map[string]any
	if rec.Header().Get("Content-Type") != "" {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}

func TestHealthz(t *testing.T) {
	h, _ := newHandler(t, 0)
	rec, body := get(t, h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["status"] != "ok" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestServerInfoReflectsConfig(t *testing.T) {
	h, cfg := newHandler(t, 3)
	ctx := context.Background()
	if err := cfg.Set(ctx, config.KeyServerName, "Our place"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := cfg.Set(ctx, config.KeyServerPassword, "hunter2"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	rec, body := get(t, h, "/api/v1/server-info")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["name"] != "Our place" {
		t.Fatalf("name = %v", body["name"])
	}
	if body["password_required"] != true {
		t.Fatalf("password_required should be true, got %v", body["password_required"])
	}
	if body["online_users"] != float64(3) {
		t.Fatalf("online_users = %v", body["online_users"])
	}
	// The password itself must never leave the server.
	if _, leaked := body["password"]; leaked {
		t.Fatal("server-info leaked the server password")
	}
}

func TestGatewayIsMountedOnWS(t *testing.T) {
	h, _ := newHandler(t, 0)
	rec, _ := get(t, h, "/ws")
	if rec.Code != http.StatusTeapot {
		t.Fatalf("/ws should reach the gateway handler, got status %d", rec.Code)
	}
}

// The logging middleware wraps the ResponseWriter. If that wrapper hides the
// underlying connection, every WebSocket upgrade fails with 501 — so the
// handler must still be hijackable through it.
func TestHandlerStaysHijackable(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	cfg, err := config.Load(ctx, store)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	hijackable := make(chan bool, 1)
	h := httpapi.Handler(httpapi.Deps{
		Config:      cfg,
		StartedAt:   time.Now(),
		OnlineUsers: func() int { return 0 },
		Gateway: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				hijackable <- false
				w.WriteHeader(http.StatusNotImplemented)
				return
			}
			hijackable <- true
			conn.Close()
		}),
	})

	srv := httptest.NewServer(h)
	defer srv.Close()

	// The response is hijacked and dropped, so a transport error here is
	// expected; only the signal from inside the handler matters.
	resp, err := srv.Client().Get(srv.URL + "/ws")
	if err == nil {
		resp.Body.Close()
	}

	select {
	case ok := <-hijackable:
		if !ok {
			t.Fatal("the handler chain is not hijackable: WebSocket upgrades would fail with 501")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler was never reached")
	}
}
