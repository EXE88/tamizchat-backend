package rooms

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
	"tamizchat/internal/textutil"
)

// Room name bounds. Short enough to fit a client sidebar, long enough to be
// descriptive.
const (
	RoomNameMinLen = 1
	RoomNameMaxLen = 40
)

// Errors the gateway maps onto protocol error codes.
var (
	ErrNotFound     = errors.New("room not found")
	ErrNameTaken    = errors.New("room name already used")
	ErrBadPassword  = errors.New("wrong room password")
	ErrFull         = errors.New("room is full")
	ErrLimitReached = errors.New("room limit reached")
	ErrNotInRoom    = errors.New("not in a room")
)

// ValidationError carries a message that is safe — and useful — to show the
// user directly, unlike an internal failure.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(err error) error { return &ValidationError{Msg: err.Error()} }

// RoomStore is the persistence the manager needs; storage.Store satisfies it.
type RoomStore interface {
	CreateRoom(ctx context.Context, r storage.Room) error
	UpdateRoom(ctx context.Context, r storage.Room) error
	DeleteRoom(ctx context.Context, id string) error
	ListRooms(ctx context.Context) ([]storage.Room, error)
	NextRoomPosition(ctx context.Context) (int, error)
}

// Broadcaster reaches every connected user, whichever room they are in.
// session.Manager satisfies it.
type Broadcaster interface {
	Broadcast(typ string, payload any, exceptUUID string)
}

// Manager owns every room on the server.
type Manager struct {
	store  RoomStore
	cfg    *config.Config
	global Broadcaster

	mu       sync.RWMutex
	rooms    map[string]*Room
	onDelete []func(roomID string)
}

// OnDelete registers a callback fired after a room is removed, so packages that
// keep per-room state — the chat history, later the uploaded files — can drop
// theirs instead of leaking it.
func (m *Manager) OnDelete(fn func(roomID string)) {
	m.mu.Lock()
	m.onDelete = append(m.onDelete, fn)
	m.mu.Unlock()
}

// NewManager loads the persisted room definitions into memory.
func NewManager(ctx context.Context, store RoomStore, cfg *config.Config, global Broadcaster) (*Manager, error) {
	defs, err := store.ListRooms(ctx)
	if err != nil {
		return nil, err
	}

	m := &Manager{store: store, cfg: cfg, global: global, rooms: make(map[string]*Room, len(defs))}
	for _, def := range defs {
		m.rooms[def.ID] = newRoom(def)
	}
	slog.Info("rooms loaded", "count", len(defs))
	return m, nil
}

// List returns every room in display order.
func (m *Manager) List() []*Room {
	m.mu.RLock()
	out := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		out = append(out, r)
	}
	m.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Definition(), out[j].Definition()
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return a.CreatedAt < b.CreatedAt
	})
	return out
}

// Views renders the room list for a client.
func (m *Manager) Views() []protocol.Room {
	rooms := m.List()
	out := make([]protocol.Room, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, r.View(true))
	}
	return out
}

// Get looks a room up by id.
func (m *Manager) Get(id string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rooms[id]
	return r, ok
}

// Count is the number of rooms on the server.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

// Create adds a room and announces it to everyone.
func (m *Manager) Create(ctx context.Context, actorUUID string, req protocol.RoomCreate) (*Room, error) {
	name, err := textutil.NormalizeName(req.Name, "نام روم", RoomNameMinLen, RoomNameMaxLen)
	if err != nil {
		return nil, invalid(err)
	}

	capacity := req.Capacity
	if capacity <= 0 {
		capacity = m.cfg.Int(config.KeyRoomsDefaultMaxUsers)
	}

	m.mu.Lock()
	if max := m.cfg.Int(config.KeyRoomsMaxPerServer); max > 0 && len(m.rooms) >= max {
		m.mu.Unlock()
		return nil, ErrLimitReached
	}
	if m.nameTakenLocked(name, "") {
		m.mu.Unlock()
		return nil, ErrNameTaken
	}
	m.mu.Unlock()

	pos, err := m.store.NextRoomPosition(ctx)
	if err != nil {
		return nil, err
	}

	def := storage.Room{
		ID:       storage.NewUUID(),
		Name:     name,
		Password: strings.TrimSpace(req.Password),
		Capacity: capacity,
		Position: pos,
	}
	if err := m.store.CreateRoom(ctx, def); err != nil {
		if errors.Is(err, storage.ErrRoomNameTaken) {
			return nil, ErrNameTaken
		}
		return nil, err
	}

	room := newRoom(def)
	m.mu.Lock()
	m.rooms[def.ID] = room
	m.mu.Unlock()

	m.global.Broadcast(protocol.TypeRoomCreated, room.View(false), actorUUID)
	slog.Info("room created", "room", name, "id", def.ID, "capacity", capacity)
	return room, nil
}

// Update edits a room definition. Absent fields are left alone.
func (m *Manager) Update(ctx context.Context, actorUUID string, req protocol.RoomUpdate) (*Room, error) {
	room, ok := m.Get(req.RoomID)
	if !ok {
		return nil, ErrNotFound
	}

	def := room.Definition()
	if req.Name != nil {
		name, err := textutil.NormalizeName(*req.Name, "نام روم", RoomNameMinLen, RoomNameMaxLen)
		if err != nil {
			return nil, invalid(err)
		}
		m.mu.RLock()
		taken := m.nameTakenLocked(name, def.ID)
		m.mu.RUnlock()
		if taken {
			return nil, ErrNameTaken
		}
		def.Name = name
	}
	if req.Password != nil {
		def.Password = strings.TrimSpace(*req.Password)
	}
	if req.Capacity != nil {
		if *req.Capacity < 1 {
			return nil, invalid(errors.New("ظرفیت روم باید حداقل ۱ باشد"))
		}
		// Lowering the capacity below the current headcount is allowed: nobody
		// is thrown out, the room simply stops accepting newcomers.
		def.Capacity = *req.Capacity
	}
	if req.Position != nil {
		def.Position = *req.Position
	}

	if err := m.store.UpdateRoom(ctx, def); err != nil {
		switch {
		case errors.Is(err, storage.ErrRoomNameTaken):
			return nil, ErrNameTaken
		case errors.Is(err, storage.ErrRoomNotFound):
			return nil, ErrNotFound
		}
		return nil, err
	}

	room.mu.Lock()
	room.def = def
	room.mu.Unlock()

	m.global.Broadcast(protocol.TypeRoomUpdated, room.View(false), actorUUID)
	slog.Info("room updated", "room", def.Name, "id", def.ID)
	return room, nil
}

// Delete removes a room, ejecting anyone still inside.
func (m *Manager) Delete(ctx context.Context, actorUUID, id string) error {
	room, ok := m.Get(id)
	if !ok {
		return ErrNotFound
	}

	if err := m.store.DeleteRoom(ctx, id); err != nil {
		if errors.Is(err, storage.ErrRoomNotFound) {
			return ErrNotFound
		}
		return err
	}

	m.mu.Lock()
	delete(m.rooms, id)
	listeners := append([]func(string){}, m.onDelete...)
	m.mu.Unlock()

	for _, fn := range listeners {
		fn(id)
	}

	// Everyone inside is moved out before the room disappears, so no client is
	// left pointing at a room that no longer exists.
	for _, s := range room.Members() {
		room.removeMember(s.ClientUUID)
		s.SetRoomID("")
		_ = s.SendMessage(protocol.TypeRoomLeft, "", protocol.RoomLeft{
			RoomID: id, Reason: protocol.ReasonRoomDeleted,
		})
	}
	room.mu.Lock()
	room.stopPurgeTimer()
	room.mu.Unlock()
	room.clearContent()

	m.global.Broadcast(protocol.TypeRoomDeleted, protocol.RoomRef{RoomID: id}, actorUUID)
	slog.Info("room deleted", "room", room.Name(), "id", id)
	return nil
}

// Join puts a session into a room, leaving whatever room it was in before.
func (m *Manager) Join(sess *session.Session, roomID, password string) (*Room, error) {
	room, ok := m.Get(roomID)
	if !ok {
		return nil, ErrNotFound
	}

	def := room.Definition()
	if def.Password != "" &&
		subtle.ConstantTimeCompare([]byte(def.Password), []byte(password)) != 1 {
		return nil, ErrBadPassword
	}

	if sess.RoomID() == roomID {
		return room, nil // already there; joining again is a no-op
	}

	// Reserve the slot and cancel any pending purge in one critical section, so
	// two clients cannot both take the last seat.
	room.mu.Lock()
	if room.def.Capacity > 0 && len(room.members) >= room.def.Capacity {
		room.mu.Unlock()
		return nil, ErrFull
	}
	room.stopPurgeTimer()
	room.members[sess.ClientUUID] = sess
	room.mu.Unlock()

	if previous := sess.RoomID(); previous != "" {
		m.leaveRoom(sess, previous, protocol.ReasonSwitchedRoom)
	}
	sess.SetRoomID(roomID)

	// Membership events go to the whole server, not just the room: every client
	// renders the full room tree with who is in each room, so everyone needs to
	// know. This is also why there is no separate user.updated here — one event
	// per move, carrying the room it happened in.
	m.global.Broadcast(protocol.TypeRoomMemberJoined, protocol.RoomMember{
		RoomID: roomID, User: sess.User(),
	}, sess.ClientUUID)

	slog.Debug("room join", "room", def.Name, "username", sess.Username())
	return room, nil
}

// Leave takes a session out of its current room.
func (m *Manager) Leave(sess *session.Session) (string, error) {
	roomID := sess.RoomID()
	if roomID == "" {
		return "", ErrNotInRoom
	}
	m.leaveRoom(sess, roomID, protocol.ReasonLeftVoluntarily)
	return roomID, nil
}

// TransferMembership hands a room membership from a replaced session to the
// reconnecting one. Without it the old session would stay in the room's member
// map forever: the room would look occupied, never purge, and the user would be
// silently outside a room they think they are in.
//
// Nothing is broadcast, because from every other member's point of view nobody
// entered or left.
func (m *Manager) TransferMembership(old, fresh *session.Session) bool {
	roomID := old.RoomID()
	if roomID == "" {
		return false
	}
	room, ok := m.Get(roomID)
	if !ok {
		old.SetRoomID("")
		return false
	}

	room.mu.Lock()
	current, present := room.members[old.ClientUUID]
	if !present || current != old {
		room.mu.Unlock()
		return false
	}
	room.members[fresh.ClientUUID] = fresh
	room.stopPurgeTimer()
	room.mu.Unlock()

	old.SetRoomID("")
	fresh.SetRoomID(roomID)
	slog.Debug("room membership transferred", "room", room.Name(), "client_uuid", fresh.ClientUUID)
	return true
}

// Disconnect removes a session that dropped off the server entirely.
func (m *Manager) Disconnect(sess *session.Session) {
	if roomID := sess.RoomID(); roomID != "" {
		m.leaveRoom(sess, roomID, protocol.ReasonDisconnected)
	}
}

// leaveRoom is the single exit path every departure goes through: leaving on
// purpose, switching rooms, and dropping off the server.
func (m *Manager) leaveRoom(sess *session.Session, roomID, reason string) {
	room, ok := m.Get(roomID)
	if !ok {
		sess.SetRoomID("")
		return
	}

	remaining, removed := room.removeMember(sess.ClientUUID)
	if !removed {
		return
	}
	if sess.RoomID() == roomID {
		sess.SetRoomID("")
	}

	m.global.Broadcast(protocol.TypeRoomMemberLeft, protocol.RoomMember{
		RoomID: roomID, User: sess.User(), Reason: reason,
	}, sess.ClientUUID)

	if remaining == 0 {
		m.schedulePurge(room)
	}
}

// schedulePurge arranges for the room's content to be dropped now that it is
// empty. The grace period exists so a momentary disconnect — or one person
// stepping out and right back in — does not wipe an active conversation.
func (m *Manager) schedulePurge(room *Room) {
	if !m.cfg.Bool(config.KeyRoomsPurgeOnEmpty) {
		return
	}

	grace := time.Duration(m.cfg.Int(config.KeyRoomsPurgeGraceSec)) * time.Second
	if grace <= 0 {
		m.purge(room)
		return
	}

	room.mu.Lock()
	room.stopPurgeTimer()
	room.purgeTimer = time.AfterFunc(grace, func() { m.purge(room) })
	room.mu.Unlock()
}

// purge drops the room's content, unless someone joined in the meantime.
func (m *Manager) purge(room *Room) {
	room.mu.Lock()
	room.purgeTimer = nil
	occupied := len(room.members) > 0
	id, name := room.def.ID, room.def.Name
	room.mu.Unlock()

	if occupied {
		return // someone came back inside the grace period
	}

	room.clearContent()
	m.global.Broadcast(protocol.TypeRoomPurged, protocol.RoomRef{RoomID: id}, "")
	slog.Info("room content purged", "room", name, "id", id)
}

// nameTakenLocked reports whether another room already uses name. The caller
// must hold m.mu.
func (m *Manager) nameTakenLocked(name, exceptID string) bool {
	want := strings.ToLower(name)
	for id, r := range m.rooms {
		if id == exceptID {
			continue
		}
		if strings.ToLower(r.Name()) == want {
			return true
		}
	}
	return false
}

// removeMember drops a member and reports how many are left.
func (r *Room) removeMember(clientUUID string) (remaining int, removed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.members[clientUUID]; !ok {
		return len(r.members), false
	}
	delete(r.members, clientUUID)
	return len(r.members), true
}

func sortUsers(users []protocol.User) {
	sort.Slice(users, func(i, j int) bool {
		return strings.ToLower(users[i].Username) < strings.ToLower(users[j].Username)
	})
}
