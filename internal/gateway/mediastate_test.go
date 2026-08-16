package gateway_test

import (
	"testing"

	"tamizchat/internal/protocol"
)

// TestMediaStateReachesTheRoom covers what the room tree has to show above
// somebody's name: their microphone, and whether they have switched their own
// speakers off.
//
// Both are the client's own report — the server neither enforces nor doubts
// them — so what matters here is that the report is broadcast to the others and
// still there for whoever joins afterwards.
func TestMediaStateReachesTheRoom(t *testing.T) {
	f := newFixture(t)

	alice, room := f.roomWith(t)
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	bob.joinRoom(room.ID, "")
	alice.expect(protocol.TypeRoomMemberJoined)

	// Bob closes his microphone and turns his speakers off.
	bob.send(protocol.TypeMediaSetState, "m1", protocol.MediaState{Mic: false, Deaf: true})

	var announced protocol.MediaStateEvent
	alice.decode(alice.expect(protocol.TypeMediaState), &announced)

	if announced.ClientUUID != uuidB || announced.RoomID != room.ID {
		t.Fatalf("the event should name Bob and his room: %+v", announced)
	}
	if announced.State.Mic || !announced.State.Deaf {
		t.Fatalf("a closed microphone and deafened speakers should arrive as such: %+v", announced.State)
	}

	// Bob's own reply carries the same thing, so his interface agrees with
	// everyone else's without waiting for a round trip.
	var mine protocol.MediaStateEvent
	bob.decode(bob.expect(protocol.TypeMediaState), &mine)
	if !mine.State.Deaf {
		t.Fatalf("the sender should be told their own state back: %+v", mine.State)
	}

	// And somebody arriving later sees it in the room tree, rather than only
	// those who happened to be watching when it changed.
	carol, _ := f.hello(t, uuidC, "Carol", "")
	carol.send(protocol.TypeRoomList, "r1", nil)

	var rooms protocol.RoomList
	carol.decode(carol.expect(protocol.TypeRooms), &rooms)

	for _, r := range rooms.Rooms {
		if r.ID != room.ID {
			continue
		}
		for _, member := range r.Members {
			if member.ClientUUID != uuidB {
				continue
			}
			if member.Media.Mic || !member.Media.Deaf {
				t.Fatalf("the room tree should carry Bob's state: %+v", member.Media)
			}
			return
		}
	}

	t.Fatal("Bob was not in the room tree at all")
}
