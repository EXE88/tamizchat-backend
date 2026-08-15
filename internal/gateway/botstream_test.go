package gateway_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"tamizchat/internal/protocol"
)

// streamURLOf pulls the URL the server handed to Ingress out of the most recent
// CreateIngress call — waitFor returns the first one, which after a second
// track would be the wrong one.
func streamURLOf(t *testing.T, lk *fakeLiveKit) string {
	t.Helper()

	lk.waitFor(t, "CreateIngress")
	call, ok := lk.lastCall("CreateIngress")
	if !ok {
		t.Fatal("no CreateIngress call")
	}

	url, _ := call.Body["url"].(string)
	if url == "" {
		t.Fatalf("no stream url in the ingress call: %+v", call.Body)
	}
	return url
}

// fetch downloads a track the way Ingress does.
func fetch(t *testing.T, url string) (int, string) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestEachTrackKeepsItsOwnFetchURL pins the bug that made a bot loop forever.
//
// The fetch token used to belong to the bot, so starting the next track revoked
// the previous one's URL. An Ingress still pulling that track got a 404, told
// the server the track had ended, and the server advanced — over and over,
// several times a second. A URL now belongs to the track it was issued for.
func TestEachTrackKeepsItsOwnFetchURL(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "01 first.mp3", "02 second.mp3"))
	alice, room := f.roomWith(t)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)

	alice.botControl(protocol.BotActionPlay, bot.ID)
	first := streamURLOf(t, lk)

	if status, body := fetch(t, first); status != http.StatusOK ||
		!strings.Contains(body, "01 first.mp3") {
		t.Fatalf("the first track should be served, got %d %q", status, body)
	}

	// Moving on must not break the URL the previous track is still being pulled
	// from, and that URL must keep serving *its own* track.
	alice.botControl(protocol.BotActionNext, bot.ID)
	second := streamURLOf(t, lk)

	if second == first {
		t.Fatal("each track needs its own url, or one revokes the other")
	}

	if status, body := fetch(t, first); status != http.StatusOK ||
		!strings.Contains(body, "01 first.mp3") {
		t.Fatalf("the first track's url stopped working: %d %q", status, body)
	}
	if status, body := fetch(t, second); status != http.StatusOK ||
		!strings.Contains(body, "02 second.mp3") {
		t.Fatalf("the second url should serve the second track, got %d %q", status, body)
	}
}

// TestDeletingABotRevokesItsFetchURLs is the other half: a url must not outlive
// the bot it belongs to.
func TestDeletingABotRevokesItsFetchURLs(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	alice, room := f.roomWith(t)
	created := alice.createBotOverSocket("DJ")

	list := alice.createPlaylist(created.ID, "Set")
	f.uploadTrack(t, alice, created.ID, list.ID, "one.mp3", []byte("fake audio"))

	alice.send(protocol.TypeBotPlaylistSelect, "ps", protocol.BotPlaylistSpec{
		BotID: created.ID, PlaylistID: list.ID,
	})
	alice.expect(protocol.TypeBotState)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: created.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)

	alice.botControl(protocol.BotActionPlay, created.ID)
	url := streamURLOf(t, lk)

	if status, _ := fetch(t, url); status != http.StatusOK {
		t.Fatalf("the track should be served while the bot exists, got %d", status)
	}

	alice.send(protocol.TypeBotDelete, "d1", protocol.BotRef{BotID: created.ID})
	alice.expect(protocol.TypeBotGone)

	if status, _ := fetch(t, url); status != http.StatusNotFound {
		t.Fatalf("a deleted bot's url must stop working, got %d", status)
	}

	if _, err := f.store.GetBot(context.Background(), created.ID); err == nil {
		t.Fatal("the bot row should be gone")
	}
}
