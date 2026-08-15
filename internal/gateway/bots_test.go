package gateway_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/media"
	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
)

// musicFolder builds a folder of fake tracks. The bytes are never decoded by
// this server — LiveKit's Ingress does that — so any content will do.
func musicFolder(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fake audio "+name), 0o600); err != nil {
			t.Fatalf("write track: %v", err)
		}
	}
	// A file that is not music must be ignored by the scan.
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("not audio"), 0o600); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	return dir
}

// addBot registers a bot the way the admin panel does, then reloads the running
// manager so it is visible without a restart.
func (f *fixture) addBot(t *testing.T, name, folder string) storage.Bot {
	t.Helper()
	ctx := context.Background()

	bot := storage.Bot{
		ID: storage.NewUUID(), Name: name, Kind: storage.BotKindMusic,
		Folder: folder, LoopQueue: true, Enabled: true,
	}
	if err := f.store.CreateBot(ctx, bot); err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if err := f.bots.Reload(ctx); err != nil {
		t.Fatalf("reload bots: %v", err)
	}
	return bot
}

// enableMediaForBots points the server at a fake LiveKit and sets the public
// host, which Ingress needs in order to fetch anything from us.
func (f *fixture) enableMediaForBots(t *testing.T, lk *fakeLiveKit) {
	t.Helper()
	f.enableMedia(t, lk)
	if err := f.cfg.Set(context.Background(), config.KeyPublicHost, f.httpURL); err != nil {
		t.Fatalf("set public host: %v", err)
	}
}

func (c *client) botControl(action, botID string) protocol.Bot {
	c.t.Helper()
	c.send(protocol.TypeBotControl, "b1", protocol.BotControl{BotID: botID, Action: action})
	var view protocol.Bot
	c.decode(c.expect(protocol.TypeBotState), &view)
	return view
}

func TestBotsAppearWithTheirQueue(t *testing.T) {
	f := newFixture(t)
	folder := musicFolder(t, "a.mp3", "b.ogg", "c.flac")
	bot := f.addBot(t, "DJ", folder)

	alice, _ := f.hello(t, uuidA, "Alice", "")
	alice.send(protocol.TypeBotList, "l1", nil)

	var list protocol.BotList
	alice.decode(alice.expect(protocol.TypeBots), &list)
	if len(list.Bots) != 1 {
		t.Fatalf("expected one bot, got %+v", list.Bots)
	}

	got := list.Bots[0]
	if got.ID != bot.ID || got.Name != "DJ" {
		t.Fatalf("unexpected bot: %+v", got)
	}
	if got.TrackCount != 3 {
		t.Fatalf("the scan should find the three audio files, got %d", got.TrackCount)
	}
	if got.State != protocol.BotIdle || got.RoomID != "" {
		t.Fatalf("a fresh bot sits nowhere: %+v", got)
	}
}

func TestBotIsAnnouncedInWelcome(t *testing.T) {
	f := newFixture(t)
	f.addBot(t, "DJ", musicFolder(t, "a.mp3"))

	_, welcome := f.hello(t, uuidA, "Alice", "")
	if len(welcome.Bots) != 1 || welcome.Bots[0].Name != "DJ" {
		t.Fatalf("the client should see the bots on connect: %+v", welcome.Bots)
	}
}

func TestMovingABotAndPlaying(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3", "two.mp3"))
	alice, room := f.roomWith(t)

	// Everyone hears about the move, not just whoever asked for it.
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	var moved protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &moved)
	if moved.RoomID != room.ID || moved.State != protocol.BotStopped {
		t.Fatalf("unexpected bot after the move: %+v", moved)
	}
	bob.expect(protocol.TypeBotState)

	playing := alice.botControl(protocol.BotActionPlay, bot.ID)
	if playing.State != protocol.BotPlaying || playing.Track == nil {
		t.Fatalf("the bot should be playing something: %+v", playing)
	}
	if playing.Track.Title != "one" {
		t.Fatalf("playback starts at the first track, got %q", playing.Track.Title)
	}
	bob.expect(protocol.TypeBotState)

	// LiveKit was asked to pull the track from this server.
	call := lk.waitFor(t, "CreateIngress")
	if call.Body["room_name"] != room.ID {
		t.Fatalf("the ingress should publish into the room: %+v", call.Body)
	}
	if identity, _ := call.Body["participant_identity"].(string); !strings.HasPrefix(identity, "bot-") {
		t.Fatalf("a bot identity must not look like a client uuid: %q", identity)
	}

	streamURL, _ := call.Body["url"].(string)
	if !strings.Contains(streamURL, "/api/v1/bot-stream/"+bot.ID) {
		t.Fatalf("unexpected stream url: %q", streamURL)
	}

	// And that URL really serves the track.
	resp, err := http.Get(streamURL)
	if err != nil {
		t.Fatalf("fetch track: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the ingress fetch should succeed, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "one.mp3") {
		t.Fatalf("the wrong track was served: %q", body)
	}
}

func TestNextAndPrevWalkTheQueue(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	// The queue is sorted by file name, which is the order the operator sees
	// in their own file manager — so the names here are numbered rather than
	// spelled out.
	bot := f.addBot(t, "DJ", musicFolder(t, "01 first.mp3", "02 second.mp3", "03 third.mp3"))
	alice, room := f.roomWith(t)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)

	if view := alice.botControl(protocol.BotActionPlay, bot.ID); view.Track.Title != "01 first" {
		t.Fatalf("playback starts at the first track, got %q", view.Track.Title)
	}
	if view := alice.botControl(protocol.BotActionNext, bot.ID); view.Track.Title != "02 second" {
		t.Fatalf("next should advance, got %q", view.Track.Title)
	}
	if view := alice.botControl(protocol.BotActionNext, bot.ID); view.Track.Title != "03 third" {
		t.Fatalf("next should advance again, got %q", view.Track.Title)
	}
	// The queue loops by default, so the end wraps to the start.
	if view := alice.botControl(protocol.BotActionNext, bot.ID); view.Track.Title != "01 first" {
		t.Fatalf("the queue should loop, got %q", view.Track.Title)
	}
	if view := alice.botControl(protocol.BotActionPrev, bot.ID); view.Track.Title != "03 third" {
		t.Fatalf("prev should wrap backwards, got %q", view.Track.Title)
	}

	// Selecting jumps straight to a track.
	alice.send(protocol.TypeBotControl, "b2", protocol.BotControl{
		BotID: bot.ID, Action: protocol.BotActionSelect, TrackIndex: 1,
	})
	var view protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &view)
	if view.Track.Title != "02 second" {
		t.Fatalf("select should jump to the chosen track, got %q", view.Track.Title)
	}

	alice.send(protocol.TypeBotControl, "b3", protocol.BotControl{
		BotID: bot.ID, Action: protocol.BotActionSelect, TrackIndex: 99,
	})
	alice.expectError(protocol.ErrInvalidInput)
}

func TestStoppingABotEndsTheIngress(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))
	alice, room := f.roomWith(t)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)
	alice.botControl(protocol.BotActionPlay, bot.ID)
	lk.waitFor(t, "CreateIngress")

	stopped := alice.botControl(protocol.BotActionStop, bot.ID)
	if stopped.State != protocol.BotStopped {
		t.Fatalf("the bot should be stopped but still in the room: %+v", stopped)
	}
	lk.waitFor(t, "DeleteIngress")

	// A stopped bot no longer serves its track: the fetch token is spent.
	f.expectStreamRefused(t, bot.ID)
}

// A track that runs out must move the queue on by itself, or a music bot would
// play exactly one song.
func TestTrackEndAdvancesTheQueue(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "01 first.mp3", "02 second.mp3"))
	alice, room := f.roomWith(t)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)
	alice.botControl(protocol.BotActionPlay, bot.ID)

	call := lk.waitFor(t, "CreateIngress")
	ingressID, _ := call.Reply["ingressId"].(string)
	if ingressID == "" {
		t.Fatal("the fake LiveKit should have returned an ingress id")
	}

	f.postWebhook(t, media.EventIngressEnded, ingressID, http.StatusOK)

	var view protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &view)
	if view.Track == nil || view.Track.Title != "02 second" {
		t.Fatalf("the bot should have moved to the next track, got %+v", view.Track)
	}
	if view.State != protocol.BotPlaying {
		t.Fatalf("and kept playing, got %q", view.State)
	}
}

// The webhook endpoint has no other authentication, so an unsigned request must
// change nothing at all.
func TestUnsignedWebhookIsRejected(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	body, _ := json.Marshal(map[string]any{
		"event":       media.EventIngressEnded,
		"ingressInfo": map[string]string{"ingressId": "made-up"},
	})

	resp, err := http.Post(f.httpURL+"/api/v1/livekit/webhook", "application/json",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an unsigned webhook should be refused, got %d", resp.StatusCode)
	}
}

func TestBotControlNeedsPermission(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	// Bob holds only the default role, which does not control bots.
	bob.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	bob.expectError(protocol.ErrForbidden)

	bob.send(protocol.TypeBotControl, "b1", protocol.BotControl{
		BotID: bot.ID, Action: protocol.BotActionPlay,
	})
	bob.expectError(protocol.ErrForbidden)

	// Reading the list is fine: bots show up in everyone's room tree.
	bob.send(protocol.TypeBotList, "l1", nil)
	bob.expect(protocol.TypeBots)
}

func TestPlayingNeedsARoomAndMusic(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))
	empty := f.addBot(t, "Empty", t.TempDir())

	alice, room := f.roomWith(t)

	// Not in a room yet.
	alice.send(protocol.TypeBotControl, "b1", protocol.BotControl{
		BotID: bot.ID, Action: protocol.BotActionPlay,
	})
	alice.expectError(protocol.ErrRoomNotFound)

	// In a room, but with nothing to play.
	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: empty.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)
	alice.send(protocol.TypeBotControl, "b2", protocol.BotControl{
		BotID: empty.ID, Action: protocol.BotActionPlay,
	})
	alice.expectError(protocol.ErrBotEmpty)
}

func TestPlayingNeedsMediaConfigured(t *testing.T) {
	f := newFixture(t)
	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))
	alice, room := f.roomWith(t)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)

	// LiveKit is off in the fixture by default.
	alice.send(protocol.TypeBotControl, "b1", protocol.BotControl{
		BotID: bot.ID, Action: protocol.BotActionPlay,
	})
	alice.expectError(protocol.ErrMediaDisabled)
}

// Playing needs a URL that LiveKit can actually reach, which the server cannot
// guess from a bare listen address.
func TestPlayingNeedsThePublicHost(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMedia(t, lk) // deliberately without the public host

	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))
	alice, room := f.roomWith(t)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)
	alice.send(protocol.TypeBotControl, "b1", protocol.BotControl{
		BotID: bot.ID, Action: protocol.BotActionPlay,
	})
	e := alice.expectError(protocol.ErrInvalidInput)
	if !strings.Contains(e.Message, "Public host") {
		t.Fatalf("the error should tell the operator what to configure: %q", e.Message)
	}
}

// The stream endpoint is the one place a path from disk reaches the network,
// so a wrong or missing token must reveal nothing.
func TestBotStreamNeedsTheCurrentToken(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))
	f.expectStreamRefused(t, bot.ID)

	resp, err := http.Get(f.httpURL + "/api/v1/bot-stream/" + bot.ID + "?token=guessed")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a made-up token should get nothing, got %d", resp.StatusCode)
	}
}

func TestDeletingARoomSendsItsBotsHome(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))
	alice, room := f.roomWith(t)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)
	alice.botControl(protocol.BotActionPlay, bot.ID)
	lk.waitFor(t, "CreateIngress")

	alice.send(protocol.TypeRoomDelete, "d1", protocol.RoomDelete{RoomID: room.ID})
	alice.expectFrames(protocol.TypeRoomLeft, protocol.TypeRoomDeleted, protocol.TypeBotState)

	lk.waitFor(t, "DeleteIngress")
	for _, view := range f.bots.Views() {
		if view.ID == bot.ID && (view.RoomID != "" || view.State != protocol.BotIdle) {
			t.Fatalf("the bot should have left the deleted room: %+v", view)
		}
	}
}

// postWebhook signs a LiveKit webhook the way LiveKit does — a JWT whose
// sha256 claim is the hash of the body — and posts it. Signing independently
// here is deliberate: it checks the server's verification against the spec
// rather than against its own signing code.
func (f *fixture) postWebhook(t *testing.T, event, ingressID string, wantStatus int) {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"event":       event,
		"ingressInfo": map[string]string{"ingressId": ingressID},
	})
	if err != nil {
		t.Fatalf("encode webhook: %v", err)
	}

	sum := sha256.Sum256(body)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss":    "test-key",
		"exp":    time.Now().Add(time.Minute).Unix(),
		"sha256": base64.StdEncoding.EncodeToString(sum[:]),
	})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)

	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte(signing))
	token := signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/livekit/webhook",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/webhook+json")
	req.Header.Set("Authorization", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("webhook status = %d, want %d", resp.StatusCode, wantStatus)
	}
}

// expectStreamRefused checks that the bot's audio is not served without a token.
func (f *fixture) expectStreamRefused(t *testing.T, botID string) {
	t.Helper()
	resp, err := http.Get(f.httpURL + "/api/v1/bot-stream/" + botID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an untokenized fetch should get nothing, got %d", resp.StatusCode)
	}
}

// ptr is the shorthand the optional fields of a BotSpec need.
func ptr[T any](v T) *T { return &v }

func TestBotCreateUpdateDeleteOverTheSocket(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeBotCreate, "c1", protocol.BotSpec{
		Name: ptr("DJ"), Color: ptr("#ff8800"), Shuffle: ptr(true),
	})
	var created protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &created)

	if created.Name != "DJ" || created.Color != "#ff8800" || !created.Shuffle {
		t.Fatalf("created bot came back wrong: %+v", created)
	}
	if created.State != protocol.BotIdle || created.TrackCount != 0 {
		t.Fatalf("a new bot should be idle with nothing to play: %+v", created)
	}
	// It has a folder of its own, and the client never learns where.
	stored, err := f.store.GetBot(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	// The library is a folder inside the bot's own storage; its playlists sit
	// beside it, so a scan of the library never picks them up twice.
	if !strings.HasSuffix(filepath.ToSlash(stored.Folder), "bots/"+created.ID+"/default") {
		t.Fatalf("bot folder = %q, want one derived from the id", stored.Folder)
	}
	if _, err := os.Stat(stored.Folder); err != nil {
		t.Fatalf("the bot's folder should exist: %v", err)
	}

	alice.send(protocol.TypeBotUpdate, "u1", protocol.BotSpec{
		BotID: created.ID, Name: ptr("Night DJ"), Shuffle: ptr(false),
	})
	var updated protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &updated)
	if updated.Name != "Night DJ" || updated.Shuffle {
		t.Fatalf("update did not apply: %+v", updated)
	}
	if updated.Color != "#ff8800" {
		t.Fatalf("a field left out of the spec must be left alone, got %+v", updated)
	}

	alice.send(protocol.TypeBotDelete, "d1", protocol.BotRef{BotID: created.ID})
	var gone protocol.BotRef
	alice.decode(alice.expect(protocol.TypeBotGone), &gone)
	if gone.BotID != created.ID {
		t.Fatalf("bot.removed named %q, want %q", gone.BotID, created.ID)
	}

	alice.send(protocol.TypeBotList, "l1", nil)
	var list protocol.BotList
	alice.decode(alice.expect(protocol.TypeBots), &list)
	if len(list.Bots) != 0 {
		t.Fatalf("the deleted bot is still listed: %+v", list.Bots)
	}
	// The folder it was given goes with it.
	if _, err := os.Stat(stored.Folder); !os.IsNotExist(err) {
		t.Fatalf("the bot's own folder should be gone, stat gave %v", err)
	}
}

func TestBotLifecycleReachesEveryone(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeBotCreate, "c1", protocol.BotSpec{Name: ptr("DJ")})
	var created protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &created)

	// Bob was not the actor, so he learns about the new bot from the broadcast.
	var seen protocol.Bot
	bob.decode(bob.expect(protocol.TypeBotState), &seen)
	if seen.ID != created.ID || seen.Name != "DJ" {
		t.Fatalf("bob saw %+v, want the new bot", seen)
	}

	alice.send(protocol.TypeBotDelete, "d1", protocol.BotRef{BotID: created.ID})
	alice.expect(protocol.TypeBotGone)

	var gone protocol.BotRef
	bob.decode(bob.expect(protocol.TypeBotGone), &gone)
	if gone.BotID != created.ID {
		t.Fatalf("bob was told %q was removed, want %q", gone.BotID, created.ID)
	}
}

func TestBotLifecycleNeedsManageBots(t *testing.T) {
	f := newFixture(t)
	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))

	// Bob holds only the default role. Controlling bots and managing them are
	// separate permissions, so granting him control must not let him delete one.
	f.defaultRolePermissionsWith(t, "control_bots")
	bob, _ := f.hello(t, uuidB, "Bob", "")

	bob.send(protocol.TypeBotCreate, "c1", protocol.BotSpec{Name: ptr("Mine")})
	bob.expectError(protocol.ErrForbidden)

	bob.send(protocol.TypeBotUpdate, "u1", protocol.BotSpec{BotID: bot.ID, Name: ptr("Mine")})
	bob.expectError(protocol.ErrForbidden)

	bob.send(protocol.TypeBotDelete, "d1", protocol.BotRef{BotID: bot.ID})
	bob.expectError(protocol.ErrForbidden)
}

func TestBotCreateRejectsABadName(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeBotCreate, "c1", protocol.BotSpec{Name: ptr("   ")})
	alice.expectError(protocol.ErrInvalidInput)

	alice.send(protocol.TypeBotCreate, "c2", protocol.BotSpec{})
	alice.expectError(protocol.ErrInvalidInput)

	alice.send(protocol.TypeBotCreate, "c3", protocol.BotSpec{Name: ptr("DJ")})
	alice.expect(protocol.TypeBotState)

	// The name is unique, exactly as it is in the panel.
	alice.send(protocol.TypeBotCreate, "c4", protocol.BotSpec{Name: ptr("dj")})
	alice.expectError(protocol.ErrBotNameUsed)
}

func TestDisablingABotStopsIt(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMediaForBots(t, lk)

	bot := f.addBot(t, "DJ", musicFolder(t, "one.mp3"))
	alice, room := f.roomWith(t)

	alice.send(protocol.TypeBotMove, "m1", protocol.BotMove{BotID: bot.ID, RoomID: room.ID})
	alice.expect(protocol.TypeBotState)
	view := alice.botControl(protocol.BotActionPlay, bot.ID)
	if view.State != protocol.BotPlaying {
		t.Fatalf("bot should be playing, got %+v", view)
	}

	alice.send(protocol.TypeBotUpdate, "u1", protocol.BotSpec{
		BotID: bot.ID, Enabled: ptr(false),
	})
	var disabled protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &disabled)
	if disabled.Enabled {
		t.Fatalf("bot is still enabled: %+v", disabled)
	}
	if disabled.State == protocol.BotPlaying {
		t.Fatalf("a disabled bot must stop playing, got %+v", disabled)
	}
	lk.waitFor(t, "DeleteIngress")
}
