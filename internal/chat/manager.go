package chat

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"tamizchat/internal/authz"
	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/ratelimit"
	"tamizchat/internal/rooms"
	"tamizchat/internal/session"
	"tamizchat/internal/textutil"
)

// Errors the gateway maps onto protocol error codes.
var (
	ErrNotInRoom        = errors.New("not in a room")
	ErrRateLimited      = errors.New("too many messages")
	ErrForbidden        = errors.New("not allowed")
	ErrStickersDisabled = errors.New("stickers are disabled")
)

// ValidationError carries a message meant to be shown to the user as-is.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// stickerPattern keeps sticker identifiers to a safe, filename-like shape. The
// sticker artwork itself lives on the client for now; the server only passes
// the identifier along.
var stickerPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// defaultHistoryLimit is used when a client asks for history without saying
// how much it wants.
const defaultHistoryLimit = 50

// typingBurst and typingPerSecond bound typing notifications. They are fixed
// rather than configurable: this is protection against a broken client, not a
// policy an admin needs to tune.
const (
	typingBurst     = 5
	typingPerSecond = 1.0
)

// Manager owns every room's chat buffer and the per-user rate limits.
type Manager struct {
	cfg    *config.Config
	rooms  *rooms.Manager
	policy authz.Policy

	mu       sync.Mutex
	buffers  map[string]*Buffer
	sendRate map[string]*ratelimit.Bucket
	typeRate map[string]*ratelimit.Bucket
}

// NewManager wires chat into the room lifecycle.
func NewManager(cfg *config.Config, roomMgr *rooms.Manager, policy authz.Policy) *Manager {
	m := &Manager{
		cfg:      cfg,
		rooms:    roomMgr,
		policy:   policy,
		buffers:  make(map[string]*Buffer),
		sendRate: make(map[string]*ratelimit.Bucket),
		typeRate: make(map[string]*ratelimit.Bucket),
	}
	// A deleted room takes its history with it.
	roomMgr.OnDelete(func(roomID string) {
		m.mu.Lock()
		delete(m.buffers, roomID)
		m.mu.Unlock()
	})
	return m
}

// Send posts a message to the sender's current room and tells the other
// members. The returned message is the caller's own confirmation.
func (m *Manager) Send(sess *session.Session, req protocol.ChatSend) (protocol.Message, error) {
	room, err := m.currentRoom(sess)
	if err != nil {
		return protocol.Message{}, err
	}

	msg := protocol.Message{
		Author:    sess.User(),
		CreatedAt: time.Now().Unix(),
	}

	switch sticker := strings.TrimSpace(req.StickerID); {
	case sticker != "":
		if !m.cfg.Bool(config.KeyChatStickersEnabled) {
			return protocol.Message{}, ErrStickersDisabled
		}
		if !stickerPattern.MatchString(sticker) {
			return protocol.Message{}, invalid("شناسهٔ استیکر معتبر نیست")
		}
		msg.Kind = protocol.MessageSticker
		msg.StickerID = sticker

	default:
		text, err := textutil.NormalizeMessage(req.Text, m.cfg.Int(config.KeyChatMaxMessageLen))
		if err != nil {
			return protocol.Message{}, &ValidationError{Msg: err.Error()}
		}
		msg.Kind = protocol.MessageText
		msg.Text = text
	}

	// The rate check comes after validation so a client cannot burn its budget
	// on messages the server was going to reject anyway.
	if !m.allowSend(sess.ClientUUID) {
		return protocol.Message{}, ErrRateLimited
	}

	stored := m.bufferFor(room).Append(msg)
	room.Broadcast(protocol.TypeChatMessage, stored, sess.ClientUUID)
	return stored, nil
}

// History returns a page of the current room's messages.
func (m *Manager) History(sess *session.Session, req protocol.ChatHistoryRequest) (protocol.ChatHistory, error) {
	room, err := m.currentRoom(sess)
	if err != nil {
		return protocol.ChatHistory{}, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if max := m.cfg.Int(config.KeyRoomsHistoryLimit); limit > max {
		limit = max
	}

	msgs, hasMore := m.bufferFor(room).History(req.BeforeSeq, limit)
	return protocol.ChatHistory{RoomID: room.ID(), Messages: msgs, HasMore: hasMore}, nil
}

// Edit changes the text of a message. Only the author may edit; moderators can
// remove a message but not put words in someone else's mouth.
func (m *Manager) Edit(sess *session.Session, req protocol.ChatEdit) (protocol.Message, error) {
	room, err := m.currentRoom(sess)
	if err != nil {
		return protocol.Message{}, err
	}

	text, err := textutil.NormalizeMessage(req.Text, m.cfg.Int(config.KeyChatMaxMessageLen))
	if err != nil {
		return protocol.Message{}, &ValidationError{Msg: err.Error()}
	}

	updated, err := m.bufferFor(room).Edit(req.MessageID, func(msg *protocol.Message) error {
		if msg.Author.ClientUUID != sess.ClientUUID {
			return ErrForbidden
		}
		if msg.Kind != protocol.MessageText {
			return invalid("فقط پیام متنی قابل ویرایش است")
		}
		msg.Text = text
		msg.EditedAt = time.Now().Unix()
		return nil
	})
	if err != nil {
		return protocol.Message{}, err
	}

	room.Broadcast(protocol.TypeChatUpdated, updated, sess.ClientUUID)
	return updated, nil
}

// Delete removes a message. Authors may delete their own; moderators may
// delete anyone's.
func (m *Manager) Delete(sess *session.Session, req protocol.ChatDelete) (protocol.ChatDeleted, error) {
	room, err := m.currentRoom(sess)
	if err != nil {
		return protocol.ChatDeleted{}, err
	}

	var byModerator bool
	removed, err := m.bufferFor(room).Delete(req.MessageID, func(msg protocol.Message) error {
		if msg.Author.ClientUUID == sess.ClientUUID {
			return nil
		}
		if !m.policy.CanModerateChat(sess.ClientUUID) {
			return ErrForbidden
		}
		byModerator = true
		return nil
	})
	if err != nil {
		return protocol.ChatDeleted{}, err
	}

	event := protocol.ChatDeleted{RoomID: room.ID(), MessageID: removed.ID}
	if byModerator {
		event.DeletedBy = sess.ClientUUID
	}
	room.Broadcast(protocol.TypeChatDeleted, event, sess.ClientUUID)
	return event, nil
}

// Typing forwards a typing hint to the rest of the room. Nothing is stored and
// no reply is sent: if the notification is dropped, the client's own timeout
// clears the indicator.
func (m *Manager) Typing(sess *session.Session, typing bool) error {
	room, err := m.currentRoom(sess)
	if err != nil {
		return err
	}
	if !m.allowTyping(sess.ClientUUID) {
		return ErrRateLimited
	}

	room.Broadcast(protocol.TypeChatTypingEvent, protocol.ChatTyping{
		RoomID:     room.ID(),
		ClientUUID: sess.ClientUUID,
		Username:   sess.Username(),
		Typing:     typing,
	}, sess.ClientUUID)
	return nil
}

// Forget drops a disconnected user's rate-limit state.
func (m *Manager) Forget(clientUUID string) {
	m.mu.Lock()
	delete(m.sendRate, clientUUID)
	delete(m.typeRate, clientUUID)
	m.mu.Unlock()
}

// currentRoom resolves the room the session is in.
func (m *Manager) currentRoom(sess *session.Session) (*rooms.Room, error) {
	roomID := sess.RoomID()
	if roomID == "" {
		return nil, ErrNotInRoom
	}
	room, ok := m.rooms.Get(roomID)
	if !ok {
		return nil, ErrNotInRoom
	}
	return room, nil
}

// bufferFor returns the room's history, creating and registering it the first
// time the room is used. Registering it as an Ephemeral is what makes the
// history vanish when the room empties.
func (m *Manager) bufferFor(room *rooms.Room) *Buffer {
	roomID := room.ID()

	m.mu.Lock()
	defer m.mu.Unlock()

	if buf, ok := m.buffers[roomID]; ok {
		return buf
	}
	buf := NewBuffer(roomID, func() int { return m.cfg.Int(config.KeyRoomsHistoryLimit) })
	m.buffers[roomID] = buf
	room.AddEphemeral(buf)
	return buf
}

func (m *Manager) allowSend(clientUUID string) bool {
	m.mu.Lock()
	bucket, ok := m.sendRate[clientUUID]
	if !ok {
		bucket = &ratelimit.Bucket{}
		m.sendRate[clientUUID] = bucket
	}
	m.mu.Unlock()

	perSecond := float64(m.cfg.Int(config.KeyChatRatePerMinute)) / 60
	return bucket.Allow(m.cfg.Int(config.KeyChatRateBurst), perSecond)
}

func (m *Manager) allowTyping(clientUUID string) bool {
	m.mu.Lock()
	bucket, ok := m.typeRate[clientUUID]
	if !ok {
		bucket = &ratelimit.Bucket{}
		m.typeRate[clientUUID] = bucket
	}
	m.mu.Unlock()

	return bucket.Allow(typingBurst, typingPerSecond)
}
