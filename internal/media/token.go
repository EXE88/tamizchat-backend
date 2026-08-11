// Package media connects TamizChat rooms to LiveKit, which carries the voice,
// video and screen share.
//
// The backend never touches media packets. It does two things: it signs the
// tokens that let a client into a LiveKit room with exactly the rights its
// roles allow, and it calls LiveKit's server API to enforce moderation — a
// kicked user must lose their microphone, not just their chat.
//
// The tokens are plain HS256 JWTs and the server API is Twirp over HTTP+JSON,
// so both are implemented here directly rather than pulling in the LiveKit
// server SDK, which would drag a full WebRTC stack into a server that never
// handles a single RTP packet.
package media

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"tamizchat/internal/storage"
)

// videoGrant is LiveKit's permission object inside the token.
type videoGrant struct {
	Room           string `json:"room,omitempty"`
	RoomJoin       bool   `json:"roomJoin,omitempty"`
	RoomAdmin      bool   `json:"roomAdmin,omitempty"`
	RoomCreate     bool   `json:"roomCreate,omitempty"`
	RoomList       bool   `json:"roomList,omitempty"`
	CanPublish     *bool  `json:"canPublish,omitempty"`
	CanSubscribe   *bool  `json:"canSubscribe,omitempty"`
	CanPublishData *bool  `json:"canPublishData,omitempty"`
	// CanPublishSources narrows publishing to specific sources, which is how
	// "may talk but may not share their screen" is expressed.
	CanPublishSources []string `json:"canPublishSources,omitempty"`
}

// claims is the JWT payload LiveKit expects.
type claims struct {
	Issuer     string     `json:"iss"`
	Subject    string     `json:"sub,omitempty"`
	Name       string     `json:"name,omitempty"`
	NotBefore  int64      `json:"nbf"`
	Expiry     int64      `json:"exp"`
	JTI        string     `json:"jti,omitempty"`
	Video      videoGrant `json:"video"`
	Metadata   string     `json:"metadata,omitempty"`
	Identity   string     `json:"-"`
	SharedRoom string     `json:"-"`
}

// Publishable track sources, as LiveKit names them.
const (
	sourceCamera     = "camera"
	sourceMicrophone = "microphone"
	sourceScreen     = "screen_share"
	sourceScreenAud  = "screen_share_audio"
)

// signToken builds a signed LiveKit access token.
func signToken(apiKey, apiSecret string, c claims) (string, error) {
	if apiKey == "" || apiSecret == "" {
		return "", ErrNotConfigured
	}

	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}

	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)

	mac := hmac.New(sha256.New, []byte(apiSecret))
	mac.Write([]byte(signing))
	return signing + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// joinToken is the token a user connects with. The grants mirror the user's
// permissions, so a client cannot talk its way into publishing by asking
// nicely: LiveKit itself refuses.
func joinToken(apiKey, apiSecret, room, identity, name string,
	rights Rights, ttl time.Duration) (string, error) {
	subscribe := true
	publish := rights.CanPublishAnything()

	grant := videoGrant{
		Room:           room,
		RoomJoin:       true,
		CanPublish:     &publish,
		CanSubscribe:   &subscribe,
		CanPublishData: boolPtr(false), // our own socket carries the data
	}
	if publish {
		grant.CanPublishSources = rights.sources()
	}

	now := time.Now()
	return signToken(apiKey, apiSecret, claims{
		Issuer:    apiKey,
		Subject:   identity,
		Name:      name,
		NotBefore: now.Add(-30 * time.Second).Unix(),
		Expiry:    now.Add(ttl).Unix(),
		JTI:       storage.NewUUID(),
		Video:     grant,
	})
}

// adminToken authorizes the server's own calls to LiveKit's room API.
func adminToken(apiKey, apiSecret string) (string, error) {
	now := time.Now()
	return signToken(apiKey, apiSecret, claims{
		Issuer:    apiKey,
		Subject:   "tamizchat-server",
		NotBefore: now.Add(-30 * time.Second).Unix(),
		Expiry:    now.Add(2 * time.Minute).Unix(),
		JTI:       storage.NewUUID(),
		Video:     videoGrant{RoomAdmin: true, RoomList: true, RoomCreate: true},
	})
}

// Rights is what a specific user may publish, derived from their roles and
// whether they are muted.
type Rights struct {
	Speak       bool
	Video       bool
	ScreenShare bool
}

// CanPublishAnything reports whether the user may publish at all.
func (r Rights) CanPublishAnything() bool {
	return r.Speak || r.Video || r.ScreenShare
}

// sources lists the LiveKit track sources the user may publish.
func (r Rights) sources() []string {
	var out []string
	if r.Speak {
		out = append(out, sourceMicrophone)
	}
	if r.Video {
		out = append(out, sourceCamera)
	}
	if r.ScreenShare {
		out = append(out, sourceScreen, sourceScreenAud)
	}
	return out
}

func boolPtr(v bool) *bool { return &v }

// decodeClaims reads a token back without verifying it. Only tests use it; the
// server never consumes tokens it issued.
func decodeClaims(token string) (claims, error) {
	var c claims
	parts := splitN(token, '.', 3)
	if len(parts) != 3 {
		return c, fmt.Errorf("malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(payload, &c)
}

func splitN(s string, sep byte, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(s) && len(out) < n-1; i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
