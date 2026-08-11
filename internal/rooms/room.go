// Package rooms owns the rooms of a server: their persistent definitions and
// their temporary, in-memory contents.
//
// The split matters. A room's definition (name, password, capacity) survives
// restarts. Everything that happens inside a room — messages, files, drawings —
// is ephemeral by design: when the last member leaves, it is thrown away.
package rooms

import (
	"log/slog"
	"sync"
	"time"

	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
)

// Ephemeral is per-room state that must vanish when the room empties. Later
// phases register the message buffer, the uploaded files and the paint board
// here, so the purge logic never has to know what it is clearing.
type Ephemeral interface {
	// Clear drops everything the room was holding.
	Clear()
}

// Room is one room: its definition plus who is inside right now.
type Room struct {
	mu         sync.RWMutex
	def        storage.Room
	members    map[string]*session.Session // keyed by client UUID
	ephemerals []Ephemeral
	purgeTimer *time.Timer
}

func newRoom(def storage.Room) *Room {
	return &Room{def: def, members: make(map[string]*session.Session)}
}

// ID is the room identifier.
func (r *Room) ID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.def.ID
}

// Name is the current room name.
func (r *Room) Name() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.def.Name
}

// Definition returns a copy of the persistent definition, password included.
// Only the admin paths should look at the password.
func (r *Room) Definition() storage.Room {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.def
}

// MemberCount is how many people are inside.
func (r *Room) MemberCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.members)
}

// Members snapshots the sessions currently inside.
func (r *Room) Members() []*session.Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*session.Session, 0, len(r.members))
	for _, s := range r.members {
		out = append(out, s)
	}
	return out
}

// AddEphemeral registers a per-room store to be cleared on purge.
func (r *Room) AddEphemeral(e Ephemeral) {
	r.mu.Lock()
	r.ephemerals = append(r.ephemerals, e)
	r.mu.Unlock()
}

// View builds the public representation of the room. withMembers is false for
// list views, where the client only needs the member count.
func (r *Room) View(withMembers bool) protocol.Room {
	r.mu.RLock()
	defer r.mu.RUnlock()

	v := protocol.Room{
		ID:          r.def.ID,
		Name:        r.def.Name,
		HasPassword: r.def.Password != "",
		Capacity:    r.def.Capacity,
		Position:    r.def.Position,
		MemberCount: len(r.members),
		Members:     []protocol.User{},
	}
	if withMembers {
		for _, s := range r.members {
			v.Members = append(v.Members, s.User())
		}
		sortUsers(v.Members)
	}
	return v
}

// Broadcast queues a frame for everyone inside the room, optionally skipping
// one client — normally the person who caused the event, who gets a reply
// correlated to their request instead.
func (r *Room) Broadcast(typ string, payload any, exceptUUID string) {
	frame, err := protocol.Encode(typ, "", payload)
	if err != nil {
		slog.Error("encode room broadcast", "type", typ, "room", r.Name(), "err", err)
		return
	}
	for _, s := range r.Members() {
		if s.ClientUUID == exceptUUID {
			continue
		}
		_ = s.Send(frame)
	}
}

// clearContent drops every registered ephemeral store.
func (r *Room) clearContent() {
	r.mu.RLock()
	stores := append([]Ephemeral{}, r.ephemerals...)
	r.mu.RUnlock()

	for _, e := range stores {
		e.Clear()
	}
}

// stopPurgeTimer cancels a pending purge, e.g. because someone came back.
// The caller must hold r.mu.
func (r *Room) stopPurgeTimer() {
	if r.purgeTimer != nil {
		r.purgeTimer.Stop()
		r.purgeTimer = nil
	}
}
