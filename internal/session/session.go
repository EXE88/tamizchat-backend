// Package session tracks who is connected right now. Sessions live only in
// memory: identity that must survive a restart belongs in storage.
package session

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"tamizchat/internal/protocol"
)

// outboundBuffer is how many frames may queue for one client before the server
// gives up on it. A client that cannot keep up is disconnected rather than
// allowed to grow the server's memory.
const outboundBuffer = 64

// ErrClosed is returned when sending to a session that is already gone.
var ErrClosed = errors.New("session closed")

// Session is one connected client.
type Session struct {
	ID         string
	ClientUUID string
	RemoteAddr string
	JoinedAt   time.Time

	mu       sync.RWMutex
	username string
	roomID   string
	roles    []string
	muted    bool
	closed   bool
	reason   string

	out  chan []byte
	done chan struct{}
	once sync.Once
}

// New creates a session in the open state. The gateway owns the returned
// session's Outbound channel and must drain it until Done closes.
func New(id, clientUUID, username, remoteAddr string) *Session {
	return &Session{
		ID:         id,
		ClientUUID: clientUUID,
		RemoteAddr: remoteAddr,
		JoinedAt:   time.Now(),
		username:   username,
		out:        make(chan []byte, outboundBuffer),
		done:       make(chan struct{}),
	}
}

// Username returns the current display name.
func (s *Session) Username() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.username
}

// SetUsername updates the display name.
func (s *Session) SetUsername(name string) {
	s.mu.Lock()
	s.username = name
	s.mu.Unlock()
}

// RoomID is the room the user is currently in ("" when in none).
func (s *Session) RoomID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.roomID
}

// SetRoomID records the user's current room. The room manager owns this value;
// nothing else should write it.
func (s *Session) SetRoomID(id string) {
	s.mu.Lock()
	s.roomID = id
	s.mu.Unlock()
}

// SetRoles caches the role ids shown to other clients. The access manager is
// the source of truth; this is a copy kept next to the session so presence
// payloads do not have to look it up on every broadcast.
func (s *Session) SetRoles(roles []string) {
	s.mu.Lock()
	s.roles = append([]string(nil), roles...)
	s.mu.Unlock()
}

// SetMuted records whether the user is currently muted.
func (s *Session) SetMuted(muted bool) {
	s.mu.Lock()
	s.muted = muted
	s.mu.Unlock()
}

// User is the public view of this session.
func (s *Session) User() protocol.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return protocol.User{
		ClientUUID: s.ClientUUID,
		Username:   s.username,
		JoinedAt:   s.JoinedAt.Unix(),
		RoomID:     s.roomID,
		Roles:      append([]string(nil), s.roles...),
		Muted:      s.muted,
	}
}

// Outbound is the queue the gateway's write pump drains.
func (s *Session) Outbound() <-chan []byte { return s.out }

// Done closes once the session has been closed.
func (s *Session) Done() <-chan struct{} { return s.done }

// Reason reports why the session ended ("" while still open).
func (s *Session) Reason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reason
}

// Send queues a pre-encoded frame. It never blocks: if the client's queue is
// full the session is closed as a slow consumer.
func (s *Session) Send(frame []byte) error {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrClosed
	}

	select {
	case s.out <- frame:
		return nil
	default:
		slog.Warn("dropping slow client", "client_uuid", s.ClientUUID, "username", s.Username())
		s.Close(protocol.ReasonSlow)
		return ErrClosed
	}
}

// SendMessage encodes and queues a frame.
func (s *Session) SendMessage(typ, id string, payload any) error {
	frame, err := protocol.Encode(typ, id, payload)
	if err != nil {
		return err
	}
	return s.Send(frame)
}

// SendError queues an error frame, correlated to a request id when known.
func (s *Session) SendError(id, code, message string) {
	_ = s.SendMessage(protocol.TypeError, id, protocol.Error{Code: code, Message: message})
}

// Close marks the session finished and wakes the gateway pumps. It is safe to
// call more than once; the first reason wins.
func (s *Session) Close(reason string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.reason = reason
		s.mu.Unlock()
		close(s.done)
	})
}

// Closed reports whether the session has ended.
func (s *Session) Closed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}
