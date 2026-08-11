package media

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"tamizchat/internal/config"
	"tamizchat/internal/storage"
)

// stubSanctions satisfies the media manager's dependency without a database.
type stubSanctions struct{}

func (stubSanctions) IsMuted(string) bool { return false }

func newTestManager(t *testing.T) *Manager {
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
	for key, value := range map[string]string{
		config.KeyLiveKitAPIKey:    "test-key",
		config.KeyLiveKitAPISecret: "test-secret",
	} {
		if err := cfg.Set(ctx, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	return New(cfg, nil, stubSanctions{})
}

func TestWebhookIsAcceptedWhenSigned(t *testing.T) {
	m := newTestManager(t)

	body, _ := json.Marshal(map[string]any{
		"event":       EventIngressEnded,
		"ingressInfo": map[string]string{"ingressId": "ingress-1", "roomName": "room-1"},
	})
	auth, err := signWebhookForTest("test-key", "test-secret", body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	event, err := m.VerifyWebhook(auth, body)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.Event != EventIngressEnded || event.Ingress.IngressID != "ingress-1" {
		t.Fatalf("unexpected event: %+v", event)
	}
}

// The webhook endpoint has no other authentication, so each of these must be
// refused — otherwise anyone who can reach the server could tell it a track
// ended, or replay a body they did not sign.
func TestWebhookRejectsEverythingUnsigned(t *testing.T) {
	m := newTestManager(t)

	body, _ := json.Marshal(map[string]any{"event": EventIngressEnded})
	good, err := signWebhookForTest("test-key", "test-secret", body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	t.Run("no header", func(t *testing.T) {
		if _, err := m.VerifyWebhook("", body); err == nil {
			t.Fatal("an unsigned webhook must be refused")
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		forged, err := signWebhookForTest("test-key", "guessed-secret", body)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := m.VerifyWebhook(forged, body); err == nil {
			t.Fatal("a webhook signed with the wrong secret must be refused")
		}
	})

	t.Run("wrong api key", func(t *testing.T) {
		other, err := signWebhookForTest("someone-elses-key", "test-secret", body)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := m.VerifyWebhook(other, body); err == nil {
			t.Fatal("a webhook from another issuer must be refused")
		}
	})

	t.Run("body swapped after signing", func(t *testing.T) {
		tampered, _ := json.Marshal(map[string]any{
			"event":       EventIngressEnded,
			"ingressInfo": map[string]string{"ingressId": "somebody-elses-ingress"},
		})
		if _, err := m.VerifyWebhook(good, tampered); err == nil {
			t.Fatal("the body hash must be checked, not just the signature")
		}
	})

	t.Run("garbage", func(t *testing.T) {
		if _, err := m.VerifyWebhook("Bearer not.a.jwt", body); err == nil {
			t.Fatal("a malformed token must be refused")
		}
	})
}

func TestWebhookNeedsCredentials(t *testing.T) {
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

	m := New(cfg, nil, stubSanctions{})
	if _, err := m.VerifyWebhook("whatever", []byte("{}")); err == nil {
		t.Fatal("a server without LiveKit credentials cannot verify anything")
	}
}
