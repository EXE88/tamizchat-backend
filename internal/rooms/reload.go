package rooms

import (
	"context"
	"log/slog"

	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
)

// Reload re-reads the room definitions from the database and reconciles them
// with what is running, announcing the differences the same way a live change
// would. It is what makes a room created in the admin panel appear immediately
// instead of after a restart.
//
// Membership is never touched except where a room disappeared: reloading must
// not throw people out of rooms that still exist.
func (m *Manager) Reload(ctx context.Context) (int, error) {
	defs, err := m.store.ListRooms(ctx)
	if err != nil {
		return 0, err
	}

	seen := make(map[string]bool, len(defs))
	var created, updated []*Room

	m.mu.Lock()
	for _, def := range defs {
		seen[def.ID] = true

		room, exists := m.rooms[def.ID]
		if !exists {
			room = newRoom(def)
			m.rooms[def.ID] = room
			created = append(created, room)
			continue
		}
		if changed(room.Definition(), def) {
			room.mu.Lock()
			room.def = def
			room.mu.Unlock()
			updated = append(updated, room)
		}
	}

	var removed []*Room
	for id, room := range m.rooms {
		if !seen[id] {
			delete(m.rooms, id)
			removed = append(removed, room)
		}
	}
	listeners := append([]func(string){}, m.onDelete...)
	m.mu.Unlock()

	for _, room := range created {
		m.global.Broadcast(protocol.TypeRoomCreated, room.View(false), "")
	}
	for _, room := range updated {
		m.global.Broadcast(protocol.TypeRoomUpdated, room.View(false), "")
	}
	for _, room := range removed {
		m.evict(room)
		for _, fn := range listeners {
			fn(room.ID())
		}
		m.global.Broadcast(protocol.TypeRoomDeleted, protocol.RoomRef{RoomID: room.ID()}, "")
	}

	if len(created)+len(updated)+len(removed) > 0 {
		slog.Info("rooms reloaded",
			"created", len(created), "updated", len(updated), "removed", len(removed))
	}
	return len(defs), nil
}

// evict moves everyone out of a room that no longer exists, so no client is
// left pointing at one.
func (m *Manager) evict(room *Room) {
	for _, sess := range room.Members() {
		room.removeMember(sess.ClientUUID)
		sess.SetRoomID("")
		sess.SetMedia(protocol.MediaState{})
		_ = sess.SendMessage(protocol.TypeRoomLeft, "", protocol.RoomLeft{
			RoomID: room.ID(), Reason: protocol.ReasonRoomDeleted,
		})
	}

	room.mu.Lock()
	room.stopPurgeTimer()
	room.mu.Unlock()
	room.clearContent()
}

// changed reports whether anything a client can see about a room differs.
func changed(a, b storage.Room) bool {
	return a.Name != b.Name ||
		a.Password != b.Password ||
		a.Capacity != b.Capacity ||
		a.Position != b.Position ||
		a.RequiredRoleID != b.RequiredRoleID
}
