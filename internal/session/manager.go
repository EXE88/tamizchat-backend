package session

import (
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"tamizchat/internal/protocol"
)

// ErrServerFull is returned when the configured user cap is reached.
var ErrServerFull = errors.New("server full")

// Manager is the registry of live sessions, keyed by the client UUID. One UUID
// means one presence: a second connection with the same UUID replaces the
// first, which is what happens when a client reconnects after a network drop
// before the server noticed the old socket was dead.
type Manager struct {
	maxUsers func() int

	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewManager builds a manager. maxUsers is read on every join so the admin can
// change the cap without a restart.
func NewManager(maxUsers func() int) *Manager {
	return &Manager{
		maxUsers: maxUsers,
		sessions: make(map[string]*Session),
	}
}

// Add registers a session. If another session already holds the same client
// UUID it is returned as replaced, already closed, and the caller should let
// its pumps unwind — no user.left is broadcast for it, because from everyone
// else's point of view the user never went away.
func (m *Manager) Add(s *Session) (replaced *Session, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if old, ok := m.sessions[s.ClientUUID]; ok {
		old.Close(protocol.ReasonReplaced)
		m.sessions[s.ClientUUID] = s
		return old, nil
	}

	if max := m.maxUsers(); max > 0 && len(m.sessions) >= max {
		return nil, ErrServerFull
	}

	m.sessions[s.ClientUUID] = s
	return nil, nil
}

// Remove deregisters a session, unless it was already replaced by a newer one
// for the same UUID. Reports whether the removal actually happened.
func (m *Manager) Remove(s *Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cur, ok := m.sessions[s.ClientUUID]; ok && cur == s {
		delete(m.sessions, s.ClientUUID)
		return true
	}
	return false
}

// Get looks a session up by client UUID.
func (m *Manager) Get(clientUUID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[clientUUID]
	return s, ok
}

// Count is the number of connected clients.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// Users lists everyone online, sorted by name for a stable client-side order.
func (m *Manager) Users() []protocol.User {
	m.mu.RLock()
	out := make([]protocol.User, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.User())
	}
	m.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username)
	})
	return out
}

// Sessions returns a snapshot of the live sessions.
func (m *Manager) Sessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// UsernameTaken reports whether someone other than exceptUUID is using name.
// Comparison is case-insensitive so "Ali" and "ali" cannot both be online.
func (m *Manager) UsernameTaken(name, exceptUUID string) bool {
	want := strings.ToLower(name)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for uuid, s := range m.sessions {
		if uuid == exceptUUID {
			continue
		}
		if strings.ToLower(s.Username()) == want {
			return true
		}
	}
	return false
}

// Broadcast queues a frame for everyone except exceptUUID (pass "" for all).
func (m *Manager) Broadcast(typ string, payload any, exceptUUID string) {
	frame, err := protocol.Encode(typ, "", payload)
	if err != nil {
		slog.Error("encode broadcast", "type", typ, "err", err)
		return
	}
	for _, s := range m.Sessions() {
		if s.ClientUUID == exceptUUID {
			continue
		}
		_ = s.Send(frame)
	}
}

// CloseAll ends every session, used during shutdown.
func (m *Manager) CloseAll(reason string) {
	for _, s := range m.Sessions() {
		s.Close(reason)
	}
}
