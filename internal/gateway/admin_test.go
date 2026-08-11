package gateway_test

import (
	"context"
	"testing"
	"time"

	"tamizchat/internal/access"
	"tamizchat/internal/authz"
	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
)

func TestWelcomeCarriesRolesAndPermissions(t *testing.T) {
	f := newFixture(t)

	_, admin := f.hello(t, uuidA, "Alice", "")
	if len(admin.Roles) != 2 {
		t.Fatalf("a fresh server should have the two built-in roles, got %d", len(admin.Roles))
	}
	if !hasString(admin.You.Roles, storage.RoleIDAdmin) {
		t.Fatalf("Alice should hold the admin role, got %v", admin.You.Roles)
	}
	if !hasString(admin.Permissions, "ban") || !hasString(admin.Permissions, "manage_rooms") {
		t.Fatalf("an admin should have the full permission set, got %v", admin.Permissions)
	}

	_, plain := f.hello(t, uuidB, "Bob", "")
	if !hasString(plain.You.Roles, storage.RoleIDDefault) {
		t.Fatalf("everyone holds the default role, got %v", plain.You.Roles)
	}
	if hasString(plain.Permissions, "ban") {
		t.Fatalf("a plain user must not have ban, got %v", plain.Permissions)
	}
	if !hasString(plain.Permissions, "send_messages") {
		t.Fatalf("the default role should allow chatting, got %v", plain.Permissions)
	}
}

func TestKickDisconnectsAndAnnounces(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)
	carol, _ := f.hello(t, uuidC, "Carol", "")
	alice.expect(protocol.TypeUserJoined)
	bob.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeAdminKick, "k1", protocol.AdminTarget{
		ClientUUID: uuidB, Reason: "بی‌ادبی",
	})

	var event protocol.Moderation
	bob.decode(bob.expect(protocol.TypeUserKicked), &event)
	if event.ClientUUID != uuidB || event.ByUUID != uuidA || event.Reason != "بی‌ادبی" {
		t.Fatalf("unexpected kick payload: %+v", event)
	}

	// Carol, a bystander, hears about it too.
	carol.decode(carol.expect(protocol.TypeUserKicked), &event)
	if event.ClientUUID != uuidB {
		t.Fatalf("bystanders should be told who was kicked, got %+v", event)
	}
	// The kick reply and the disconnect that follows it arrive independently.
	alice.expectFrames(protocol.TypeAdminOK, protocol.TypeUserLeft)

	waitForSessions(t, f, 2)

	// A kick is not a ban: Bob may come straight back.
	f.hello(t, uuidB, "Bob", "")
}

func TestKickRequiresPermission(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	bob.send(protocol.TypeAdminKick, "k1", protocol.AdminTarget{ClientUUID: uuidA})
	bob.expectError(protocol.ErrForbidden)
}

// Two administrators of equal rank must not be able to remove each other.
func TestEqualRankCannotBeKicked(t *testing.T) {
	f := newFixture(t)
	f.makeAdmin(t, uuidB)

	alice, _ := f.hello(t, uuidA, "Alice", "")
	f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeAdminKick, "k1", protocol.AdminTarget{ClientUUID: uuidB})
	alice.expectError(protocol.ErrOutranked)
}

func TestKickingYourselfIsRejected(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeAdminKick, "k1", protocol.AdminTarget{ClientUUID: uuidA})
	alice.expectError(protocol.ErrInvalidInput)
}

func TestKickingSomeoneOfflineIsReported(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeAdminKick, "k1", protocol.AdminTarget{ClientUUID: uuidB})
	alice.expectError(protocol.ErrUserNotFound)
}

func TestBanBlocksReconnection(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeAdminBan, "b1", protocol.AdminTarget{
		ClientUUID: uuidB, Reason: "اسپم",
	})
	bob.expect(protocol.TypeUserBanned)
	alice.expectFrames(protocol.TypeAdminOK, protocol.TypeUserLeft)
	waitForSessions(t, f, 1)

	// Coming back is refused, with the reason attached.
	c := f.dial(t)
	c.send(protocol.TypeHello, "h1", protocol.Hello{ClientUUID: uuidB, Username: "Bob"})
	e := c.expectError(protocol.ErrBanned)
	if e.Message == "" {
		t.Fatal("a banned client should be told why")
	}

	// And the ban shows up in the admin's list.
	alice.send(protocol.TypeAdminSanctions, "s1", nil)
	var list protocol.SanctionList
	alice.decode(alice.expect(protocol.TypeAdminSanctionList), &list)
	if len(list.Sanctions) != 1 || list.Sanctions[0].ClientUUID != uuidB ||
		list.Sanctions[0].Kind != storage.SanctionBan {
		t.Fatalf("unexpected sanction list: %+v", list.Sanctions)
	}
	if list.Sanctions[0].ExpiresAt != 0 {
		t.Fatalf("a ban with no duration should be permanent, got %d", list.Sanctions[0].ExpiresAt)
	}
}

func TestTemporaryBanExpires(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeAdminBan, "b1", protocol.AdminTarget{
		ClientUUID: uuidB, DurationSec: 1,
	})
	var event protocol.Moderation
	bob.decode(bob.expect(protocol.TypeUserBanned), &event)
	if event.ExpiresAt == 0 {
		t.Fatal("a timed ban should carry its expiry")
	}
	alice.expectFrames(protocol.TypeAdminOK, protocol.TypeUserLeft)
	waitForSessions(t, f, 1)

	// Straight away the ban still applies.
	c := f.dial(t)
	c.send(protocol.TypeHello, "h1", protocol.Hello{ClientUUID: uuidB, Username: "Bob"})
	c.expectError(protocol.ErrBanned)

	time.Sleep(1100 * time.Millisecond)
	f.hello(t, uuidB, "Bob", "")
}

func TestUnbanLetsTheUserBack(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeAdminBan, "b1", protocol.AdminTarget{ClientUUID: uuidB})
	bob.expect(protocol.TypeUserBanned)
	alice.expectFrames(protocol.TypeAdminOK, protocol.TypeUserLeft)
	waitForSessions(t, f, 1)

	alice.send(protocol.TypeAdminUnban, "u1", protocol.AdminTarget{ClientUUID: uuidB})
	alice.expect(protocol.TypeAdminOK)

	f.hello(t, uuidB, "Bob", "")

	// Lifting a ban that is not there is reported rather than silently ignored.
	alice.expect(protocol.TypeUserJoined)
	alice.send(protocol.TypeAdminUnban, "u2", protocol.AdminTarget{ClientUUID: uuidC})
	alice.expectError(protocol.ErrUserNotFound)
}

func TestMuteSilencesWithoutDisconnecting(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	bob.sendText("before the mute")
	alice.expect(protocol.TypeChatMessage)

	alice.send(protocol.TypeAdminMute, "m1", protocol.AdminTarget{
		ClientUUID: uuidB, Reason: "شلوغ‌کاری",
	})
	var event protocol.Moderation
	bob.decode(bob.expect(protocol.TypeUserMuted), &event)
	if event.ClientUUID != uuidB || event.Reason != "شلوغ‌کاری" {
		t.Fatalf("unexpected mute payload: %+v", event)
	}
	alice.expect(protocol.TypeAdminOK)

	// Still connected, but silenced.
	bob.send(protocol.TypeChatSend, "m2", protocol.ChatSend{Text: "after the mute"})
	bob.expectError(protocol.ErrMuted)

	bob.send(protocol.TypePing, "p1", nil)
	bob.expect(protocol.TypePong)

	alice.send(protocol.TypeAdminUnmute, "u1", protocol.AdminTarget{ClientUUID: uuidB})
	bob.expect(protocol.TypeUserUnmuted)
	alice.expect(protocol.TypeAdminOK)

	bob.sendText("speaking again")
	alice.expect(protocol.TypeChatMessage)
}

func TestMuteSurvivesReconnect(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeAdminMute, "m1", protocol.AdminTarget{ClientUUID: uuidB})
	bob.expect(protocol.TypeUserMuted)
	alice.expect(protocol.TypeAdminOK)

	// A muted user must not be able to shake the mute off by reconnecting.
	fresh, welcome := f.hello(t, uuidB, "Bob", "")
	if !welcome.You.Muted {
		t.Fatal("the reconnected session should still be muted")
	}
	fresh.joinRoom(room.ID, "")
	fresh.send(protocol.TypeChatSend, "m2", protocol.ChatSend{Text: "sneaky"})
	fresh.expectError(protocol.ErrMuted)
}

func TestAdminMoveIgnoresPasswordAndCapacity(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	locked := alice.createRoom("Locked", "secret", 1)

	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)
	alice.joinRoom(locked.ID, "") // admin bypasses the password; the room is now full

	bob.expect(protocol.TypeRoomMemberJoined)
	bob.send(protocol.TypeRoomJoin, "j1", protocol.RoomJoin{RoomID: locked.ID, Password: "secret"})
	bob.expectError(protocol.ErrRoomFull)

	alice.send(protocol.TypeAdminMove, "mv1", protocol.AdminMove{
		ClientUUID: uuidB, RoomID: locked.ID,
	})

	var joined protocol.RoomJoined
	bob.decode(bob.expect(protocol.TypeRoomJoined), &joined)
	if joined.Room.ID != locked.ID {
		t.Fatalf("Bob should have been placed in the locked room, got %+v", joined.Room)
	}
	alice.expect(protocol.TypeRoomMemberJoined)
	alice.expect(protocol.TypeAdminOK)
}

func TestMoveRequiresPermission(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	bob.send(protocol.TypeAdminMove, "mv1", protocol.AdminMove{ClientUUID: uuidA, RoomID: room.ID})
	bob.expectError(protocol.ErrForbidden)
}

func TestRoleLockedRoomRequiresTheRole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	name := "ویژه"
	priority := 10
	perms := authz.PermSendMessages
	special, err := f.access.CreateRole(ctx, roleSpec(name, priority, perms))
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	alice.send(protocol.TypeRoomCreate, "c1", protocol.RoomCreate{
		Name: "اتاق ویژه", RequiredRoleID: special.ID,
	})
	var room protocol.Room
	alice.decode(alice.expect(protocol.TypeRoomCreated), &room)
	if room.RequiredRoleID != special.ID {
		t.Fatalf("the lock should be stored on the room, got %+v", room)
	}

	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)
	bob.send(protocol.TypeRoomJoin, "j1", protocol.RoomJoin{RoomID: room.ID})
	bob.expectError(protocol.ErrRoleRequired)

	// Once granted the role, Bob gets in.
	alice.send(protocol.TypeAdminRoleGrant, "g1", protocol.RoleAssignment{
		ClientUUID: uuidB, RoleID: special.ID,
	})
	bob.expect(protocol.TypeUserRolesChanged)
	alice.expectFrames(protocol.TypeUserUpdated, protocol.TypeAdminOK)

	bob.joinRoom(room.ID, "")
}

func TestRoomCannotRequireAnUnknownRole(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeRoomCreate, "c1", protocol.RoomCreate{
		Name: "اتاق", RequiredRoleID: "no-such-role",
	})
	alice.expectError(protocol.ErrRoleNotFound)
}

func TestRoleCrudOverTheWire(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	name := "ناظر"
	priority := 50
	perms := []string{"moderate_chat", "kick"}
	alice.send(protocol.TypeAdminRoleCreate, "r1", protocol.RoleSpec{
		Name: &name, Priority: &priority, Permissions: &perms,
	})
	var role protocol.Role
	alice.decode(alice.expect(protocol.TypeAdminRole), &role)
	if role.Name != "ناظر" || role.Priority != 50 {
		t.Fatalf("unexpected role: %+v", role)
	}
	if len(role.Permissions) != 2 || !hasString(role.Permissions, "kick") {
		t.Fatalf("unexpected permissions: %v", role.Permissions)
	}

	alice.send(protocol.TypeAdminRoleList, "r2", nil)
	var roles []protocol.Role
	alice.decode(alice.expect(protocol.TypeAdminRoles), &roles)
	if len(roles) != 3 {
		t.Fatalf("expected three roles, got %d", len(roles))
	}

	alice.send(protocol.TypeAdminRoleDelete, "r3", protocol.RoleRef{RoleID: role.ID})
	alice.expect(protocol.TypeAdminRoleGone)

	// The built-in roles are protected: removing them could lock everyone out.
	alice.send(protocol.TypeAdminRoleDelete, "r4", protocol.RoleRef{RoleID: storage.RoleIDAdmin})
	alice.expectError(protocol.ErrRoleProtected)
}

// An administrator must not be able to escalate past the person who appointed
// them, in either of the two obvious ways.
func TestRolesCannotBeUsedToEscalate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	name := "ناظر"
	priority := 10
	moderator, err := f.access.CreateRole(ctx, roleSpec(name, priority,
		authz.PermSendMessages|authz.PermManageRoles))
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := f.access.Grant(ctx, "", uuidB, moderator.ID); err != nil {
		t.Fatalf("grant role: %v", err)
	}

	bob, _ := f.hello(t, uuidB, "Bob", "")

	// A role at or above one's own rank.
	tooStrong := "قوی"
	highPriority := 10
	bob.send(protocol.TypeAdminRoleCreate, "r1", protocol.RoleSpec{
		Name: &tooStrong, Priority: &highPriority,
	})
	bob.expectError(protocol.ErrOutranked)

	// A role carrying a permission the creator does not hold.
	weak := "ضعیف"
	lowPriority := 5
	perms := []string{"ban"}
	bob.send(protocol.TypeAdminRoleCreate, "r2", protocol.RoleSpec{
		Name: &weak, Priority: &lowPriority, Permissions: &perms,
	})
	bob.expectError(protocol.ErrForbidden)

	// And handing out the admin role is refused for the same reason.
	bob.send(protocol.TypeAdminRoleGrant, "g1", protocol.RoleAssignment{
		ClientUUID: uuidC, RoleID: storage.RoleIDAdmin,
	})
	bob.expectError(protocol.ErrOutranked)
}

func TestModerationIsLogged(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.send(protocol.TypeAdminKick, "k1", protocol.AdminTarget{
		ClientUUID: uuidB, Reason: "تست",
	})
	bob.expect(protocol.TypeUserKicked)
	alice.expectFrames(protocol.TypeAdminOK, protocol.TypeUserLeft)

	entries, err := f.store.RecentModLog(context.Background(), 10)
	if err != nil {
		t.Fatalf("read mod log: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one log entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Action != "kick" || e.ActorUUID != uuidA || e.TargetUUID != uuidB || e.Detail != "تست" {
		t.Fatalf("unexpected log entry: %+v", e)
	}
}

func roleSpec(name string, priority int, perms authz.Permission) access.RoleSpec {
	return access.RoleSpec{Name: &name, Priority: &priority, Permissions: &perms}
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// waitForSessions blocks until the server has released the disconnected
// sessions, which happens asynchronously once the socket unwinds.
func waitForSessions(t *testing.T, f *fixture, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for f.sessions.Count() != want {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d sessions, still %d", want, f.sessions.Count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
