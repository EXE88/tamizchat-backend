package gateway_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/rooms"
)

// createRoom asks the server for a new room over the wire and returns its view.
func (c *client) createRoom(name, password string, capacity int) protocol.Room {
	c.t.Helper()
	c.send(protocol.TypeRoomCreate, "c1", protocol.RoomCreate{
		Name: name, Password: password, Capacity: capacity,
	})
	var room protocol.Room
	c.decode(c.expect(protocol.TypeRoomCreated), &room)
	return room
}

func (c *client) joinRoom(roomID, password string) protocol.RoomJoined {
	c.t.Helper()
	c.send(protocol.TypeRoomJoin, "j1", protocol.RoomJoin{RoomID: roomID, Password: password})
	var joined protocol.RoomJoined
	c.decode(c.expect(protocol.TypeRoomJoined), &joined)
	return joined
}

// expectFrames reads until every wanted frame type has arrived, ignoring the
// order between them. Some server actions fan out several independent frames
// whose relative order is not part of the contract.
func (c *client) expectFrames(types ...string) map[string]protocol.Envelope {
	c.t.Helper()
	pending := map[string]bool{}
	for _, t := range types {
		pending[t] = true
	}

	got := make(map[string]protocol.Envelope, len(types))
	for len(pending) > 0 {
		env := c.recv()
		if !pending[env.Type] {
			c.t.Fatalf("unexpected frame %q while waiting for %v", env.Type, types)
		}
		got[env.Type] = env
		delete(pending, env.Type)
	}
	return got
}

func (c *client) expectError(code string) protocol.Error {
	c.t.Helper()
	var e protocol.Error
	c.decode(c.expect(protocol.TypeError), &e)
	if e.Code != code {
		c.t.Fatalf("expected error %s, got %s (%s)", code, e.Code, e.Message)
	}
	return e
}

func TestRoomCreateListAndPersist(t *testing.T) {
	f := newFixture(t)
	alice, welcome := f.hello(t, uuidA, "Alice", "")
	if len(welcome.Rooms) != 0 {
		t.Fatalf("a fresh server should have no rooms, got %d", len(welcome.Rooms))
	}

	room := alice.createRoom("  General   chat  ", "", 0)
	if room.Name != "General chat" {
		t.Fatalf("room name should be normalized, got %q", room.Name)
	}
	if room.Capacity != f.cfg.Int(config.KeyRoomsDefaultMaxUsers) {
		t.Fatalf("capacity 0 should fall back to the server default, got %d", room.Capacity)
	}
	if room.HasPassword {
		t.Fatal("room should be open")
	}

	alice.send(protocol.TypeRoomList, "l1", nil)
	var list protocol.RoomList
	alice.decode(alice.expect(protocol.TypeRooms), &list)
	if len(list.Rooms) != 1 || list.Rooms[0].ID != room.ID {
		t.Fatalf("unexpected room list: %+v", list.Rooms)
	}

	// The definition must survive: it is the one thing about a room that is
	// not ephemeral.
	stored, err := f.store.ListRooms(context.Background())
	if err != nil {
		t.Fatalf("list rooms: %v", err)
	}
	if len(stored) != 1 || stored[0].Name != "General chat" {
		t.Fatalf("room was not persisted: %+v", stored)
	}
}

func TestRoomCreationIsAnnouncedAndGated(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.createRoom("Lobby", "", 0)
	var seen protocol.Room
	bob.decode(bob.expect(protocol.TypeRoomCreated), &seen)
	if seen.Name != "Lobby" {
		t.Fatalf("Bob should have been told about the new room, got %+v", seen)
	}

	// Bob holds only the default role, which does not include room management.
	bob.send(protocol.TypeRoomCreate, "c2", protocol.RoomCreate{Name: "Secret"})
	bob.expectError(protocol.ErrForbidden)
}

func TestRoomNameMustBeUnique(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.createRoom("Lobby", "", 0)
	alice.send(protocol.TypeRoomCreate, "c2", protocol.RoomCreate{Name: "lobby"})
	alice.expectError(protocol.ErrRoomNameTaken)
}

func TestRoomLimitIsEnforced(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyRoomsMaxPerServer, "1"); err != nil {
		t.Fatalf("set room limit: %v", err)
	}
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.createRoom("First", "", 0)
	alice.send(protocol.TypeRoomCreate, "c2", protocol.RoomCreate{Name: "Second"})
	alice.expectError(protocol.ErrRoomLimit)
}

func TestRoomJoinRequiresCorrectPassword(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Private", "letmein", 0)
	if !room.HasPassword {
		t.Fatal("room should report that it has a password")
	}

	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	bob.send(protocol.TypeRoomJoin, "j0", protocol.RoomJoin{RoomID: room.ID, Password: "nope"})
	bob.expectError(protocol.ErrRoomPassword)

	joined := bob.joinRoom(room.ID, "letmein")
	if joined.Room.MemberCount != 1 {
		t.Fatalf("expected 1 member, got %d", joined.Room.MemberCount)
	}
}

// An administrator holding bypass_room_password gets in without knowing it —
// the point of the permission.
func TestRoomPasswordCanBeBypassedWithPermission(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Private", "letmein", 0)

	joined := alice.joinRoom(room.ID, "")
	if joined.Room.ID != room.ID {
		t.Fatalf("an admin should get in without the password, got %+v", joined.Room)
	}
}

func TestRoomJoinIsAnnouncedToMembersAndServer(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	room := alice.createRoom("Lobby", "", 0)
	bob.expect(protocol.TypeRoomCreated)
	alice.joinRoom(room.ID, "")

	// Bob is not in that room, but he still renders the full room tree, so he
	// is told who entered which room.
	var member protocol.RoomMember
	bob.decode(bob.expect(protocol.TypeRoomMemberJoined), &member)
	if member.RoomID != room.ID || member.User.ClientUUID != uuidA {
		t.Fatalf("unexpected member event: %+v", member)
	}
	if member.User.RoomID != room.ID {
		t.Fatalf("the user payload should carry the new room, got %q", member.User.RoomID)
	}

	bob.joinRoom(room.ID, "")
	alice.decode(alice.expect(protocol.TypeRoomMemberJoined), &member)
	if member.User.ClientUUID != uuidB {
		t.Fatalf("unexpected member event: %+v", member)
	}
}

func TestRoomCapacityIsEnforced(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	room := alice.createRoom("Duo", "", 1)
	bob.expect(protocol.TypeRoomCreated)
	alice.joinRoom(room.ID, "")
	bob.expect(protocol.TypeRoomMemberJoined)

	bob.send(protocol.TypeRoomJoin, "j2", protocol.RoomJoin{RoomID: room.ID})
	bob.expectError(protocol.ErrRoomFull)
}

func TestSwitchingRoomsLeavesThePreviousOne(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	first := alice.createRoom("First", "", 0)
	second := alice.createRoom("Second", "", 0)
	bob.expect(protocol.TypeRoomCreated)
	bob.expect(protocol.TypeRoomCreated)

	bob.joinRoom(first.ID, "")
	alice.expect(protocol.TypeRoomMemberJoined)
	alice.joinRoom(first.ID, "")
	bob.expect(protocol.TypeRoomMemberJoined)

	alice.send(protocol.TypeRoomJoin, "j3", protocol.RoomJoin{RoomID: second.ID})
	var joined protocol.RoomJoined
	alice.decode(alice.expect(protocol.TypeRoomJoined), &joined)
	if joined.Room.ID != second.ID {
		t.Fatalf("expected to be in the second room, got %s", joined.Room.ID)
	}

	// Bob sees Alice leave the first room and enter the second one.
	frames := bob.expectFrames(protocol.TypeRoomMemberLeft, protocol.TypeRoomMemberJoined)
	var left protocol.RoomMember
	bob.decode(frames[protocol.TypeRoomMemberLeft], &left)
	if left.RoomID != first.ID || left.Reason != protocol.ReasonSwitchedRoom {
		t.Fatalf("unexpected leave event: %+v", left)
	}
	var entered protocol.RoomMember
	bob.decode(frames[protocol.TypeRoomMemberJoined], &entered)
	if entered.RoomID != second.ID {
		t.Fatalf("unexpected join event: %+v", entered)
	}

	if r, ok := f.rooms.Get(first.ID); !ok || r.MemberCount() != 1 {
		t.Fatalf("first room should hold only Bob")
	}
}

func TestLeaveWithoutRoomIsRejected(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeRoomLeave, "x", nil)
	alice.expectError(protocol.ErrNotInRoom)
}

func TestRoomUpdateChangesDefinition(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Old", "", 10)

	newName := "New"
	newCap := 5
	alice.send(protocol.TypeRoomUpdate, "u1", protocol.RoomUpdate{
		RoomID: room.ID, Name: &newName, Capacity: &newCap,
	})
	var updated protocol.Room
	alice.decode(alice.expect(protocol.TypeRoomUpdated), &updated)
	if updated.Name != "New" || updated.Capacity != 5 {
		t.Fatalf("unexpected room after update: %+v", updated)
	}

	stored, err := f.store.GetRoom(context.Background(), room.ID)
	if err != nil {
		t.Fatalf("get room: %v", err)
	}
	if stored.Name != "New" || stored.Capacity != 5 {
		t.Fatalf("update was not persisted: %+v", stored)
	}
}

func TestRoomDeleteEjectsMembers(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	room := alice.createRoom("Doomed", "", 0)
	bob.expect(protocol.TypeRoomCreated)
	bob.joinRoom(room.ID, "")
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.send(protocol.TypeRoomDelete, "d1", protocol.RoomDelete{RoomID: room.ID})

	// Bob is thrown out with a reason before the room disappears.
	var left protocol.RoomLeft
	bob.decode(bob.expect(protocol.TypeRoomLeft), &left)
	if left.RoomID != room.ID || left.Reason != protocol.ReasonRoomDeleted {
		t.Fatalf("unexpected eject payload: %+v", left)
	}
	// room.left already told Bob he is roomless, so no redundant self-update.
	bob.expect(protocol.TypeRoomDeleted)

	if f.rooms.Count() != 0 {
		t.Fatalf("room should be gone, %d left", f.rooms.Count())
	}
	if _, err := f.store.GetRoom(context.Background(), room.ID); err == nil {
		t.Fatal("room definition should have been deleted")
	}
}

func TestDeletingAMissingRoomIsReported(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeRoomDelete, "d1", protocol.RoomDelete{RoomID: "no-such-room"})
	alice.expectError(protocol.ErrRoomNotFound)
}

// The whole point of the ephemeral design: when the last member walks out, the
// room's content is dropped.
func TestContentIsPurgedWhenLastMemberLeaves(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyRoomsPurgeGraceSec, "0"); err != nil {
		t.Fatalf("set grace: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Temp", "", 0)
	alice.joinRoom(room.ID, "")

	live, ok := f.rooms.Get(room.ID)
	if !ok {
		t.Fatal("room should exist")
	}
	content := &fakeContent{}
	live.AddEphemeral(content)

	alice.send(protocol.TypeRoomLeave, "l1", nil)
	alice.expectFrames(protocol.TypeRoomLeft, protocol.TypeRoomPurged)

	if !content.cleared() {
		t.Fatal("room content should have been cleared")
	}
}

// A user who steps out and comes straight back must not lose the conversation.
func TestPurgeIsCancelledIfSomeoneReturnsInTime(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyRoomsPurgeGraceSec, "2"); err != nil {
		t.Fatalf("set grace: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Temp", "", 0)
	alice.joinRoom(room.ID, "")

	live, _ := f.rooms.Get(room.ID)
	content := &fakeContent{}
	live.AddEphemeral(content)

	alice.send(protocol.TypeRoomLeave, "l1", nil)
	alice.expect(protocol.TypeRoomLeft)
	alice.joinRoom(room.ID, "")

	time.Sleep(2500 * time.Millisecond)
	if content.cleared() {
		t.Fatal("content should have survived: the room was re-entered within the grace period")
	}
}

func TestPurgeCanBeDisabled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.cfg.Set(ctx, config.KeyRoomsPurgeOnEmpty, "false"); err != nil {
		t.Fatalf("set purge: %v", err)
	}
	if err := f.cfg.Set(ctx, config.KeyRoomsPurgeGraceSec, "0"); err != nil {
		t.Fatalf("set grace: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Kept", "", 0)
	alice.joinRoom(room.ID, "")

	live, _ := f.rooms.Get(room.ID)
	content := &fakeContent{}
	live.AddEphemeral(content)

	alice.send(protocol.TypeRoomLeave, "l1", nil)
	alice.expect(protocol.TypeRoomLeft)

	time.Sleep(200 * time.Millisecond)
	if content.cleared() {
		t.Fatal("content should be kept when purging is disabled")
	}
}

// Disconnecting must free the room slot, otherwise a room slowly fills up with
// ghosts and never purges.
func TestDisconnectLeavesTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	room := alice.createRoom("Lobby", "", 0)
	bob.expect(protocol.TypeRoomCreated)
	alice.joinRoom(room.ID, "")
	bob.expect(protocol.TypeRoomMemberJoined)
	bob.joinRoom(room.ID, "")
	alice.expect(protocol.TypeRoomMemberJoined)

	bob.conn.Close(websocket.StatusNormalClosure, "bye")

	var left protocol.RoomMember
	alice.decode(alice.expect(protocol.TypeRoomMemberLeft), &left)
	if left.User.ClientUUID != uuidB || left.Reason != protocol.ReasonDisconnected {
		t.Fatalf("unexpected leave event: %+v", left)
	}

	live, _ := f.rooms.Get(room.ID)
	if live.MemberCount() != 1 {
		t.Fatalf("room should hold only Alice, got %d", live.MemberCount())
	}
}

// A reconnect must carry the room membership over, or the room would look
// occupied by a session that no longer exists.
func TestReconnectKeepsRoomMembership(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	_, welcome := f.hello(t, uuidA, "Alice", "")
	if welcome.You.RoomID != room.ID {
		t.Fatalf("reconnecting client should still be in the room, got %q", welcome.You.RoomID)
	}

	live, _ := f.rooms.Get(room.ID)
	if live.MemberCount() != 1 {
		t.Fatalf("room should hold exactly one member, got %d", live.MemberCount())
	}
}

func TestRoomNameIsValidated(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	for _, bad := range []string{"", "   ", "bad\nname", "room‮name"} {
		alice.send(protocol.TypeRoomCreate, "c", protocol.RoomCreate{Name: bad})
		alice.expectError(protocol.ErrRoomInvalidName)
	}
}

func TestRoomsSurviveRestart(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Persistent", "pw", 7)

	// A fresh manager over the same database is what a restart looks like.
	reloaded, err := rooms.NewManager(context.Background(), f.store, f.cfg, f.sessions, f.access)
	if err != nil {
		t.Fatalf("reload rooms: %v", err)
	}
	live, ok := reloaded.Get(room.ID)
	if !ok {
		t.Fatal("room should have been reloaded")
	}
	def := live.Definition()
	if def.Name != "Persistent" || def.Password != "pw" || def.Capacity != 7 {
		t.Fatalf("reloaded definition is wrong: %+v", def)
	}
	if live.MemberCount() != 0 {
		t.Fatal("membership must not survive a restart")
	}
}

// fakeContent stands in for the message buffer, file store and paint board that
// later phases will register on a room.
type fakeContent struct {
	mu   sync.Mutex
	gone bool
}

func (f *fakeContent) Clear() {
	f.mu.Lock()
	f.gone = true
	f.mu.Unlock()
}

func (f *fakeContent) cleared() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gone
}
