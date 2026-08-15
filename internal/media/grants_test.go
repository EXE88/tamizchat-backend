package media

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBotTokenPublishesAndNothingElse pins what a music bot is allowed to do.
//
// A bot joins LiveKit as a participant of its own and publishes its music. It
// must be able to send a microphone source and nothing else — no camera, no
// screen — and it must not subscribe at all: a bot has no reason to hear the
// room, and not subscribing keeps its cost flat however many people are in
// there.
func TestBotTokenPublishesAndNothingElse(t *testing.T) {
	token, err := botToken("APIkey", "secret-that-is-long-enough-for-hs256",
		"room-1", "bot-42", "DJ", time.Hour)
	if err != nil {
		t.Fatalf("bot token: %v", err)
	}

	grant := decodeGrant(t, token)

	if grant.Room != "room-1" || !grant.RoomJoin {
		t.Fatalf("the token should admit the bot to its room: %+v", grant)
	}
	if grant.CanPublish == nil || !*grant.CanPublish {
		t.Fatal("a bot that cannot publish has no purpose")
	}
	if grant.CanSubscribe == nil || *grant.CanSubscribe {
		t.Fatal("a bot must not subscribe to anybody")
	}
	if len(grant.CanPublishSources) != 1 || grant.CanPublishSources[0] != sourceMicrophone {
		t.Fatalf("a bot publishes audio only: %+v", grant.CanPublishSources)
	}
}

// TestAdminTokenIsRoomAdminOnly: the server's own API token administers rooms.
func TestAdminTokenIsRoomAdminOnly(t *testing.T) {
	token, err := adminToken("APIkey", "secret-that-is-long-enough-for-hs256")
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}

	grant := decodeGrant(t, token)
	if !grant.RoomAdmin || !grant.RoomCreate || !grant.RoomList {
		t.Fatalf("the room grants must stay as they were: %+v", grant)
	}
}

func decodeGrant(t *testing.T, token string) videoGrant {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("a JWT has three parts, got %d", len(parts))
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}

	var payload struct {
		Video videoGrant `json:"video"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	return payload.Video
}
