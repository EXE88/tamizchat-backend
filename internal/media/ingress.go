package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/storage"
)

// Ingress input types, as LiveKit names them in its protobuf enum.
const (
	// inputURL makes the Ingress service pull from a URL we serve. It is the
	// only input type TamizChat uses: the bot's audio never passes through this
	// server as media, only as an ordinary file download that Ingress fetches.
	inputURL = "URL_INPUT"
)

// IngressInfo is the part of LiveKit's reply we care about.
type IngressInfo struct {
	IngressID string `json:"ingressId"`
	Name      string `json:"name"`
	RoomName  string `json:"roomName"`
	State     struct {
		Status string `json:"status"`
	} `json:"state"`
}

// CreateIngress asks LiveKit to pull url and publish it into a room as a
// participant. The returned id is what stops it again.
func (m *Manager) CreateIngress(ctx context.Context, roomID, identity, name, url string) (string, error) {
	if !m.Enabled() {
		return "", ErrDisabled
	}

	body := map[string]any{
		"input_type":           inputURL,
		"name":                 name,
		"room_name":            roomID,
		"participant_identity": identity,
		"participant_name":     name,
		"url":                  url,
		"enable_transcoding":   true,
		// The bot publishes sound only; a music player has no camera.
		"audio": map[string]any{
			"name":   "music",
			"source": "SOURCE_UNKNOWN",
		},
	}

	var info IngressInfo
	if err := m.api.callInto(ctx, m.base(),
		m.cfg.String(config.KeyLiveKitAPIKey),
		m.cfg.String(config.KeyLiveKitAPISecret),
		"livekit.Ingress", "CreateIngress", body, &info); err != nil {
		return "", err
	}
	if info.IngressID == "" {
		return "", errors.New("livekit returned an ingress without an id")
	}
	return info.IngressID, nil
}

// DeleteIngress stops a running ingress.
func (m *Manager) DeleteIngress(ctx context.Context, ingressID string) error {
	if !m.Enabled() || ingressID == "" {
		return nil
	}
	return m.api.call(ctx, m.base(),
		m.cfg.String(config.KeyLiveKitAPIKey),
		m.cfg.String(config.KeyLiveKitAPISecret),
		"livekit.Ingress", "DeleteIngress", map[string]string{"ingress_id": ingressID})
}

// WebhookEvent is the payload LiveKit posts to us. Only the fields TamizChat
// reacts to are decoded.
type WebhookEvent struct {
	Event   string `json:"event"`
	Ingress struct {
		IngressID string `json:"ingressId"`
		RoomName  string `json:"roomName"`
	} `json:"ingressInfo"`
	Room struct {
		Name string `json:"name"`
	} `json:"room"`
	ID string `json:"id"`
}

// Webhook event names.
const (
	EventIngressStarted = "ingress_started"
	EventIngressEnded   = "ingress_ended"
)

// ErrBadWebhook is returned when a webhook cannot be trusted.
var ErrBadWebhook = errors.New("webhook signature is not valid")

// VerifyWebhook checks that a webhook really came from the configured LiveKit
// and decodes it.
//
// LiveKit signs the request with a JWT in the Authorization header whose
// "sha256" claim is the hash of the body. Verifying both is what stops anyone
// who can reach this endpoint from telling the server that a track ended — or
// from replaying a body they did not sign.
func (m *Manager) VerifyWebhook(authHeader string, body []byte) (WebhookEvent, error) {
	var event WebhookEvent

	secret := m.cfg.String(config.KeyLiveKitAPISecret)
	apiKey := m.cfg.String(config.KeyLiveKitAPIKey)
	if secret == "" || apiKey == "" {
		return event, ErrNotConfigured
	}

	token := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authHeader), "Bearer "))
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return event, ErrBadWebhook
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[2])) != 1 {
		return event, ErrBadWebhook
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return event, ErrBadWebhook
	}

	var claims struct {
		Issuer string `json:"iss"`
		Expiry int64  `json:"exp"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return event, ErrBadWebhook
	}
	if claims.Issuer != apiKey {
		return event, ErrBadWebhook
	}
	if claims.Expiry != 0 && time.Now().Unix() > claims.Expiry {
		return event, ErrBadWebhook
	}

	sum := sha256.Sum256(body)
	if !hashMatches(claims.SHA256, sum[:]) {
		return event, ErrBadWebhook
	}

	if err := json.Unmarshal(body, &event); err != nil {
		return event, fmt.Errorf("decode webhook: %w", err)
	}
	return event, nil
}

// hashMatches compares the claimed body hash, accepting both base64 spellings
// LiveKit versions have used.
func hashMatches(claimed string, sum []byte) bool {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if subtle.ConstantTimeCompare([]byte(enc.EncodeToString(sum)), []byte(claimed)) == 1 {
			return true
		}
	}
	return false
}

// signWebhookForTest builds a webhook Authorization header the way LiveKit
// would. It exists so tests can exercise the verification path.
func signWebhookForTest(apiKey, apiSecret string, body []byte) (string, error) {
	sum := sha256.Sum256(body)
	return signToken(apiKey, apiSecret, claims{
		Issuer:    apiKey,
		NotBefore: time.Now().Add(-time.Minute).Unix(),
		Expiry:    time.Now().Add(time.Minute).Unix(),
		JTI:       storage.NewUUID(),
		SHA256:    base64.StdEncoding.EncodeToString(sum[:]),
	})
}
