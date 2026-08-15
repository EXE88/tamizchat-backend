package media

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestAdminTokenCarriesIngressAdmin pins a grant a fake LiveKit cannot check
// for us.
//
// The server's own API token used to carry roomAdmin only. Every room call
// worked, so the tests passed and the fake never complained — but a real
// LiveKit answers CreateIngress with 401 "permissions denied" unless the token
// says ingressAdmin, which meant a music bot could never play a single note.
func TestAdminTokenCarriesIngressAdmin(t *testing.T) {
	token, err := adminToken("APIkey", "secret-that-is-long-enough-for-hs256")
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("a JWT has three parts, got %d", len(parts))
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}

	var payload struct {
		Video struct {
			RoomAdmin    bool `json:"roomAdmin"`
			RoomCreate   bool `json:"roomCreate"`
			RoomList     bool `json:"roomList"`
			IngressAdmin bool `json:"ingressAdmin"`
		} `json:"video"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse claims: %v", err)
	}

	if !payload.Video.IngressAdmin {
		t.Fatal("the server token must carry ingressAdmin, or music bots cannot publish")
	}
	if !payload.Video.RoomAdmin || !payload.Video.RoomCreate || !payload.Video.RoomList {
		t.Fatalf("the room grants must stay as they were: %+v", payload.Video)
	}
}
