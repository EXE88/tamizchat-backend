package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
)

// fakeLiveKit records the server API calls TamizChat makes, so the moderation
// path can be tested without running a real media server.
type fakeLiveKit struct {
	srv *httptest.Server

	mu      sync.Mutex
	calls   []liveKitCall
	created int
}

// next numbers the ingresses this fake hands out.
func (f *fakeLiveKit) next() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	return f.created
}

type liveKitCall struct {
	Method string
	Body   map[string]any
	Reply  map[string]any
	Auth   string
}

func newFakeLiveKit(t *testing.T) *fakeLiveKit {
	t.Helper()
	f := &fakeLiveKit{}

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)

		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]

		// CreateIngress is the one call whose reply matters: the id it returns
		// is what stops that ingress again.
		reply := map[string]any{}
		if method == "CreateIngress" {
			reply["ingressId"] = "ingress-" + strconv.Itoa(f.next())
			reply["roomName"] = body["room_name"]
		}

		f.mu.Lock()
		f.calls = append(f.calls, liveKitCall{
			Method: method,
			Body:   body,
			Reply:  reply,
			Auth:   r.Header.Get("Authorization"),
		})
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// lastCall returns the most recent call to a method, if any.
func (f *fakeLiveKit) lastCall(method string) (liveKitCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i], true
		}
	}
	return liveKitCall{}, false
}

// waitFor blocks until a call to method arrives, and returns it.
func (f *fakeLiveKit) waitFor(t *testing.T, method string) liveKitCall {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, c := range f.calls {
			if c.Method == method {
				f.mu.Unlock()
				return c
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("LiveKit was never asked to %s; calls so far: %v", method, f.methods())
	return liveKitCall{}
}

func (f *fakeLiveKit) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Method)
	}
	return out
}

// enableMedia points the server at the fake LiveKit.
func (f *fixture) enableMedia(t *testing.T, lk *fakeLiveKit) {
	t.Helper()
	ctx := context.Background()
	for key, value := range map[string]string{
		config.KeyLiveKitURL:       lk.srv.URL,
		config.KeyLiveKitAPIKey:    "test-key",
		config.KeyLiveKitAPISecret: "test-secret",
		config.KeyLiveKitEnabled:   "true",
	} {
		if err := f.cfg.Set(ctx, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
}

func TestMediaTokenIsIssuedForTheCurrentRoom(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMedia(t, lk)

	alice, room := f.roomWith(t)

	alice.send(protocol.TypeMediaToken, "m1", nil)
	var creds protocol.MediaToken
	alice.decode(alice.expect(protocol.TypeMediaCredentials), &creds)

	if creds.Room != room.ID || creds.Identity != uuidA {
		t.Fatalf("unexpected credentials: %+v", creds)
	}
	if creds.URL != lk.srv.URL {
		t.Fatalf("the client should be sent to the configured LiveKit: %q", creds.URL)
	}
	if strings.Count(creds.Token, ".") != 2 {
		t.Fatalf("the token should be a JWT, got %q", creds.Token)
	}
	if creds.ExpiresAt <= time.Now().Unix() {
		t.Fatal("the token is already expired")
	}
	// The admin holds every permission.
	if !creds.CanSpeak || !creds.CanPublishVideo || !creds.CanShareScreen {
		t.Fatalf("an admin should be allowed to publish everything: %+v", creds)
	}
}

func TestMediaTokenRequiresARoom(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMedia(t, lk)

	alice, _ := f.hello(t, uuidA, "Alice", "")
	alice.send(protocol.TypeMediaToken, "m1", nil)
	alice.expectError(protocol.ErrNotInRoom)
}

func TestMediaCanBeDisabled(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.roomWith(t)

	// The fixture leaves LiveKit unconfigured, which is the default.
	alice.send(protocol.TypeMediaToken, "m1", nil)
	alice.expectError(protocol.ErrMediaDisabled)

	_, welcome := f.hello(t, uuidB, "Bob", "")
	if welcome.Limits.MediaEnabled {
		t.Fatal("welcome should tell the client that media is off")
	}
}

// A user without the speak permission still gets a token — they can listen —
// but the token must not let them publish.
func TestTokenGrantsFollowPermissions(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMedia(t, lk)

	f.defaultRolePermissionsWithout(t, "speak")
	f.defaultRolePermissionsWithout(t, "share_screen")

	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	bob.send(protocol.TypeMediaToken, "m1", nil)
	var creds protocol.MediaToken
	bob.decode(bob.expect(protocol.TypeMediaCredentials), &creds)

	if creds.CanSpeak || creds.CanShareScreen {
		t.Fatalf("Bob lost those permissions: %+v", creds)
	}
	if !creds.CanPublishVideo {
		t.Fatalf("Bob kept the camera permission: %+v", creds)
	}
	if creds.Token == "" {
		t.Fatal("a listener still needs a token to hear the room")
	}
}

// A server-side mute must take the microphone away, not just the chat.
func TestMuteRemovesPublishingRights(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMedia(t, lk)

	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.send(protocol.TypeAdminMute, "m1", protocol.AdminTarget{ClientUUID: uuidB})
	bob.expect(protocol.TypeUserMuted)
	alice.expect(protocol.TypeAdminOK)

	// LiveKit is told to revoke publishing for the participant already in the
	// session — waiting for their next token would leave them talking.
	call := lk.waitFor(t, "UpdateParticipant")
	if call.Body["room"] != room.ID || call.Body["identity"] != uuidB {
		t.Fatalf("unexpected UpdateParticipant: %+v", call.Body)
	}
	permission, _ := call.Body["permission"].(map[string]any)
	if permission["canPublish"] != false {
		t.Fatalf("a muted participant must not be allowed to publish: %+v", permission)
	}
	if !strings.HasPrefix(call.Auth, "Bearer ") {
		t.Fatalf("the server API call must be authorized, got %q", call.Auth)
	}

	// And a fresh token carries the same verdict.
	bob.send(protocol.TypeMediaToken, "m2", nil)
	var creds protocol.MediaToken
	bob.decode(bob.expect(protocol.TypeMediaCredentials), &creds)
	if creds.CanSpeak || creds.CanPublishVideo || creds.CanShareScreen {
		t.Fatalf("a muted user's token must grant nothing: %+v", creds)
	}
}

func TestKickEndsTheMediaSession(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMedia(t, lk)

	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.send(protocol.TypeAdminKick, "k1", protocol.AdminTarget{ClientUUID: uuidB})
	bob.expect(protocol.TypeUserKicked)

	call := lk.waitFor(t, "RemoveParticipant")
	if call.Body["room"] != room.ID || call.Body["identity"] != uuidB {
		t.Fatalf("unexpected RemoveParticipant: %+v", call.Body)
	}
}

// Leaving a room in any way ends the media session for it.
func TestLeavingARoomEndsTheMediaSession(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMedia(t, lk)

	alice, room := f.roomWith(t)
	alice.send(protocol.TypeRoomLeave, "l1", nil)
	alice.expect(protocol.TypeRoomLeft)

	call := lk.waitFor(t, "RemoveParticipant")
	if call.Body["room"] != room.ID || call.Body["identity"] != uuidA {
		t.Fatalf("unexpected RemoveParticipant: %+v", call.Body)
	}
}

func TestDeletingARoomClosesTheLiveKitRoom(t *testing.T) {
	f := newFixture(t)
	lk := newFakeLiveKit(t)
	f.enableMedia(t, lk)

	alice, room := f.roomWith(t)
	alice.send(protocol.TypeRoomDelete, "d1", protocol.RoomDelete{RoomID: room.ID})
	alice.expectFrames(protocol.TypeRoomLeft, protocol.TypeRoomDeleted)

	call := lk.waitFor(t, "DeleteRoom")
	if call.Body["room"] != room.ID {
		t.Fatalf("unexpected DeleteRoom: %+v", call.Body)
	}
}

func TestMediaStateIsSharedWithTheServer(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.send(protocol.TypeMediaSetState, "s1", protocol.MediaState{Mic: true, Screen: true})
	alice.expect(protocol.TypeMediaState)

	var event protocol.MediaStateEvent
	bob.decode(bob.expect(protocol.TypeMediaState), &event)
	if event.ClientUUID != uuidA || event.RoomID != room.ID {
		t.Fatalf("unexpected media state event: %+v", event)
	}
	if !event.State.Mic || event.State.Cam || !event.State.Screen {
		t.Fatalf("unexpected state: %+v", event.State)
	}

	// A newcomer sees the state in the room listing rather than having to wait
	// for the next change.
	_, welcome := f.hello(t, uuidC, "Carol", "")
	for _, r := range welcome.Rooms {
		if r.ID != room.ID {
			continue
		}
		for _, u := range r.Members {
			if u.ClientUUID == uuidA && !u.Media.Mic {
				t.Fatalf("the room listing should carry the media state: %+v", u)
			}
		}
	}
}

// The icons belong to the room the user was in; leaving must clear them.
func TestMediaStateResetsOnLeavingTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.send(protocol.TypeMediaSetState, "s1", protocol.MediaState{Mic: true})
	alice.expect(protocol.TypeMediaState)
	bob.expect(protocol.TypeMediaState)

	alice.send(protocol.TypeRoomLeave, "l1", nil)
	alice.expect(protocol.TypeRoomLeft)

	var left protocol.RoomMember
	bob.decode(bob.expect(protocol.TypeRoomMemberLeft), &left)
	if left.User.Media.Mic {
		t.Fatalf("the microphone icon should be cleared on leaving: %+v", left.User)
	}
}

func TestMediaStateRequiresARoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeMediaSetState, "s1", protocol.MediaState{Mic: true})
	alice.expectError(protocol.ErrNotInRoom)
}
