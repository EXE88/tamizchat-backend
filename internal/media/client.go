package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotConfigured is returned when LiveKit is off or missing its credentials.
var ErrNotConfigured = errors.New("livekit is not configured")

// apiTimeout bounds a call to LiveKit. Media control is best effort: the chat
// server must never block on it.
const apiTimeout = 5 * time.Second

// client speaks LiveKit's RoomService, which is Twirp over HTTP with JSON
// bodies: POST /twirp/livekit.RoomService/<Method>.
type client struct {
	http *http.Client
}

func newClient() *client {
	return &client{http: &http.Client{Timeout: apiTimeout}}
}

// call posts a request to one RoomService method.
func (c *client) call(ctx context.Context, base, apiKey, apiSecret, method string, body any) error {
	if base == "" || apiKey == "" || apiSecret == "" {
		return ErrNotConfigured
	}

	token, err := adminToken(apiKey, apiSecret)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	url := strings.TrimRight(base, "/") + "/twirp/livekit.RoomService/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("livekit %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("livekit %s: status %d: %s", method, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}

// removeParticipant disconnects someone from a LiveKit room.
func (c *client) removeParticipant(ctx context.Context, base, key, secret, room, identity string) error {
	return c.call(ctx, base, key, secret, "RemoveParticipant", map[string]string{
		"room":     room,
		"identity": identity,
	})
}

// setPermission changes what a participant already in a room may publish.
// Revoking publishing is how a server-side mute reaches someone who is already
// speaking: LiveKit unpublishes their tracks for us.
func (c *client) setPermission(ctx context.Context, base, key, secret, room, identity string, rights Rights) error {
	permission := map[string]any{
		"canSubscribe":   true,
		"canPublish":     rights.CanPublishAnything(),
		"canPublishData": false,
	}
	if sources := rights.sources(); len(sources) > 0 {
		permission["canPublishSources"] = sources
	}

	return c.call(ctx, base, key, secret, "UpdateParticipant", map[string]any{
		"room":       room,
		"identity":   identity,
		"permission": permission,
	})
}

// deleteRoom closes a LiveKit room and disconnects everyone in it.
func (c *client) deleteRoom(ctx context.Context, base, key, secret, room string) error {
	return c.call(ctx, base, key, secret, "DeleteRoom", map[string]string{"room": room})
}

// httpBase turns the configured client URL into the base for the server API.
// Operators configure one address — the ws:// one their client connects to —
// and the HTTP API lives on the same host and port.
func httpBase(rawURL string) string {
	url := strings.TrimSpace(rawURL)
	switch {
	case strings.HasPrefix(url, "wss://"):
		return "https://" + strings.TrimPrefix(url, "wss://")
	case strings.HasPrefix(url, "ws://"):
		return "http://" + strings.TrimPrefix(url, "ws://")
	case url == "":
		return ""
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"):
		return url
	default:
		return "http://" + url
	}
}
