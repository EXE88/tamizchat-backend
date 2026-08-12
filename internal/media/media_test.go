package media

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestTokenIsSignedAndCarriesTheGrant(t *testing.T) {
	const (
		key    = "APIkey123"
		secret = "a-very-secret-value"
	)

	// A non-ASCII display name on purpose: it has to survive the JWT round trip.
	token, err := joinToken(key, secret, "room-7", "client-uuid", "دانیال",
		Rights{Speak: true}, 10*time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("a JWT has three parts, got %d", len(parts))
	}

	// Verify the signature the way LiveKit will.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if parts[2] != want {
		t.Fatal("the signature does not verify with the API secret")
	}

	c, err := decodeClaims(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.Issuer != key {
		t.Fatalf("issuer should be the API key, got %q", c.Issuer)
	}
	if c.Subject != "client-uuid" {
		t.Fatalf("the identity should be the client uuid, got %q", c.Subject)
	}
	if c.Name != "دانیال" {
		t.Fatalf("unexpected display name %q", c.Name)
	}
	if !c.Video.RoomJoin || c.Video.Room != "room-7" {
		t.Fatalf("unexpected grant: %+v", c.Video)
	}
	if c.Expiry <= time.Now().Unix() {
		t.Fatal("the token is already expired")
	}
}

// The grants must mirror the user's permissions: a client that asks to publish
// something it may not is refused by LiveKit itself, not merely by our UI.
func TestGrantsFollowRights(t *testing.T) {
	cases := []struct {
		name    string
		rights  Rights
		publish bool
		sources []string
	}{
		{"listener", Rights{}, false, nil},
		{"voice only", Rights{Speak: true}, true, []string{"microphone"}},
		{"voice and camera", Rights{Speak: true, Video: true}, true,
			[]string{"microphone", "camera"}},
		{"screen share adds its audio", Rights{ScreenShare: true}, true,
			[]string{"screen_share", "screen_share_audio"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, err := joinToken("k", "s", "room", "id", "name", tc.rights, time.Minute)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			c, err := decodeClaims(token)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if c.Video.CanPublish == nil || *c.Video.CanPublish != tc.publish {
				t.Fatalf("canPublish should be %v, got %v", tc.publish, c.Video.CanPublish)
			}
			if c.Video.CanSubscribe == nil || !*c.Video.CanSubscribe {
				t.Fatal("everyone may listen, even without publishing rights")
			}
			if c.Video.CanPublishData == nil || *c.Video.CanPublishData {
				t.Fatal("data channels stay closed: our own socket carries the data")
			}
			if strings.Join(c.Video.CanPublishSources, ",") != strings.Join(tc.sources, ",") {
				t.Fatalf("sources = %v, want %v", c.Video.CanPublishSources, tc.sources)
			}
		})
	}
}

func TestTokenNeedsCredentials(t *testing.T) {
	if _, err := joinToken("", "", "room", "id", "name", Rights{Speak: true}, time.Minute); err == nil {
		t.Fatal("signing without an API key should fail")
	}
}

func TestAdminTokenIsRoomAdmin(t *testing.T) {
	token, err := adminToken("k", "s")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	c, err := decodeClaims(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !c.Video.RoomAdmin || c.Video.RoomJoin {
		t.Fatalf("the server's own token administers rooms, it does not join them: %+v", c.Video)
	}
}

// Operators configure one address — the one their client dials — and the
// server API lives on the same host.
func TestHTTPBaseIsDerivedFromTheClientURL(t *testing.T) {
	cases := map[string]string{
		"ws://127.0.0.1:7880":  "http://127.0.0.1:7880",
		"wss://media.example":  "https://media.example",
		"http://127.0.0.1:788": "http://127.0.0.1:788",
		"media.example:7880":   "http://media.example:7880",
		"":                     "",
	}
	for in, want := range cases {
		if got := httpBase(in); got != want {
			t.Fatalf("httpBase(%q) = %q, want %q", in, got, want)
		}
	}
}
