package paint

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"tamizchat/internal/authz"
	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/ratelimit"
	"tamizchat/internal/rooms"
	"tamizchat/internal/session"
)

// Errors the gateway maps onto protocol error codes.
var (
	ErrDisabled    = errors.New("paint board is disabled")
	ErrForbidden   = errors.New("missing permission")
	ErrNotInRoom   = errors.New("not in a room")
	ErrRateLimited = errors.New("drawing too fast")
	ErrMuted       = errors.New("user is muted")
)

// ValidationError carries a message meant to be shown to the user as-is.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// maxColorLen bounds the colour string; it is passed through to clients as-is,
// so it must stay small and simple.
const maxColorLen = 32

// Sanctions reports who is silenced. A muted user is muted on the board too:
// scribbling over everyone is the same disruption as shouting.
type Sanctions interface {
	IsMuted(clientUUID string) bool
}

// Manager owns every room's board and the per-user drawing rate limits.
type Manager struct {
	cfg       *config.Config
	rooms     *rooms.Manager
	policy    authz.Policy
	sanctions Sanctions

	mu     sync.Mutex
	boards map[string]*Board
	rate   map[string]*ratelimit.Bucket
}

// NewManager wires the boards into the room lifecycle.
func NewManager(cfg *config.Config, roomMgr *rooms.Manager, policy authz.Policy,
	sanctions Sanctions) *Manager {
	m := &Manager{
		cfg:       cfg,
		rooms:     roomMgr,
		policy:    policy,
		sanctions: sanctions,
		boards:    make(map[string]*Board),
		rate:      make(map[string]*ratelimit.Bucket),
	}

	roomMgr.OnDelete(func(roomID string) {
		m.mu.Lock()
		delete(m.boards, roomID)
		m.mu.Unlock()
	})
	return m
}

// Begin opens a stroke and tells the rest of the room.
func (m *Manager) Begin(sess *session.Session, req protocol.PaintBegin) (protocol.Stroke, error) {
	room, err := m.drawableRoom(sess)
	if err != nil {
		return protocol.Stroke{}, err
	}
	if err := validateBegin(&req); err != nil {
		return protocol.Stroke{}, err
	}
	if !m.allow(sess.ClientUUID) {
		return protocol.Stroke{}, ErrRateLimited
	}

	stroke, err := m.boardFor(room).Begin(sess.ClientUUID, req)
	if err != nil {
		return protocol.Stroke{}, err
	}

	room.Broadcast(protocol.TypePaintStarted, stroke, sess.ClientUUID)
	return stroke, nil
}

// Append extends a stroke that is still being drawn.
func (m *Manager) Append(sess *session.Session, req protocol.PaintAppend) error {
	room, err := m.drawableRoom(sess)
	if err != nil {
		return err
	}
	if !m.allow(sess.ClientUUID) {
		return ErrRateLimited
	}

	stored, err := m.boardFor(room).Append(sess.ClientUUID, req.StrokeID, req.Points)
	if err != nil {
		return err
	}
	if len(stored) == 0 {
		return nil // the stroke is already at its maximum length
	}

	room.Broadcast(protocol.TypePaintAppended, protocol.PaintAppend{
		StrokeID: req.StrokeID,
		Points:   stored,
	}, sess.ClientUUID)
	return nil
}

// End closes a stroke.
func (m *Manager) End(sess *session.Session, req protocol.PaintEnd) error {
	room, err := m.drawableRoom(sess)
	if err != nil {
		return err
	}
	if err := m.boardFor(room).End(sess.ClientUUID, req.StrokeID); err != nil {
		return err
	}

	room.Broadcast(protocol.TypePaintEnded, req, sess.ClientUUID)
	return nil
}

// Undo removes the caller's last stroke.
func (m *Manager) Undo(sess *session.Session) (protocol.PaintUndo, error) {
	room, err := m.drawableRoom(sess)
	if err != nil {
		return protocol.PaintUndo{}, err
	}

	strokeID, err := m.boardFor(room).Undo(sess.ClientUUID)
	if err != nil {
		return protocol.PaintUndo{}, err
	}

	event := protocol.PaintUndo{StrokeID: strokeID, RoomID: room.ID()}
	room.Broadcast(protocol.TypePaintUndone, event, sess.ClientUUID)
	return event, nil
}

// Clear wipes strokes. Scope "mine" removes only the caller's own work and
// needs nothing beyond the paint permission; scope "all" wipes everyone's and
// needs moderation rights, because it destroys other people's work.
func (m *Manager) Clear(sess *session.Session, req protocol.PaintClear) (protocol.PaintCleared, error) {
	room, err := m.drawableRoom(sess)
	if err != nil {
		return protocol.PaintCleared{}, err
	}

	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = protocol.ClearMine
	}

	board := m.boardFor(room)
	switch scope {
	case protocol.ClearMine:
		board.ClearAuthor(sess.ClientUUID)
	case protocol.ClearAll:
		if !m.policy.Can(sess.ClientUUID, authz.PermModerateChat) {
			return protocol.PaintCleared{}, ErrForbidden
		}
		board.ClearAll()
	default:
		return protocol.PaintCleared{}, &ValidationError{Msg: "دامنهٔ پاک‌کردن نامعتبر است"}
	}

	event := protocol.PaintCleared{RoomID: room.ID(), Scope: scope, By: sess.ClientUUID}
	room.Broadcast(protocol.TypePaintCleared, event, sess.ClientUUID)
	return event, nil
}

// State returns the whole board, which is how a client that just walked in
// catches up with what is already drawn.
func (m *Manager) State(sess *session.Session) (protocol.PaintState, error) {
	if !m.cfg.Bool(config.KeyPaintEnabled) {
		return protocol.PaintState{}, ErrDisabled
	}
	room, ok := m.currentRoom(sess)
	if !ok {
		return protocol.PaintState{}, ErrNotInRoom
	}

	return protocol.PaintState{
		RoomID:     room.ID(),
		Strokes:    m.boardFor(room).Snapshot(),
		MaxStrokes: m.cfg.Int(config.KeyPaintMaxStrokes),
	}, nil
}

// Forget drops a disconnected user's rate-limit state.
func (m *Manager) Forget(clientUUID string) {
	m.mu.Lock()
	delete(m.rate, clientUUID)
	m.mu.Unlock()
}

// StrokeCount reports how many strokes a room's board holds, for tests and the
// admin panel.
func (m *Manager) StrokeCount(roomID string) int {
	m.mu.Lock()
	board, ok := m.boards[roomID]
	m.mu.Unlock()
	if !ok {
		return 0
	}
	return board.Len()
}

// drawableRoom resolves the caller's room and checks they may draw in it.
func (m *Manager) drawableRoom(sess *session.Session) (*rooms.Room, error) {
	if !m.cfg.Bool(config.KeyPaintEnabled) {
		return nil, ErrDisabled
	}
	room, ok := m.currentRoom(sess)
	if !ok {
		return nil, ErrNotInRoom
	}
	if !m.policy.Can(sess.ClientUUID, authz.PermPaint) {
		return nil, ErrForbidden
	}
	if m.sanctions.IsMuted(sess.ClientUUID) {
		return nil, ErrMuted
	}
	return room, nil
}

func (m *Manager) currentRoom(sess *session.Session) (*rooms.Room, bool) {
	roomID := sess.RoomID()
	if roomID == "" {
		return nil, false
	}
	return m.rooms.Get(roomID)
}

// boardFor returns the room's board, creating and registering it on first use.
// Registering it as an Ephemeral is what makes the drawing disappear when the
// room empties.
func (m *Manager) boardFor(room *rooms.Room) *Board {
	roomID := room.ID()

	m.mu.Lock()
	defer m.mu.Unlock()

	if b, ok := m.boards[roomID]; ok {
		return b
	}
	b := NewBoard(roomID, func() int { return m.cfg.Int(config.KeyPaintMaxStrokes) })
	m.boards[roomID] = b
	room.AddEphemeral(b)
	return b
}

func (m *Manager) allow(clientUUID string) bool {
	m.mu.Lock()
	bucket, ok := m.rate[clientUUID]
	if !ok {
		bucket = &ratelimit.Bucket{}
		m.rate[clientUUID] = bucket
	}
	m.mu.Unlock()

	return bucket.Allow(m.cfg.Int(config.KeyPaintRateBurst),
		float64(m.cfg.Int(config.KeyPaintRatePerSecond)))
}

// validateBegin normalizes and checks the shape of a new stroke.
func validateBegin(req *protocol.PaintBegin) error {
	switch req.Tool {
	case "":
		req.Tool = protocol.ToolPen
	case protocol.ToolPen, protocol.ToolEraser, protocol.ToolLine,
		protocol.ToolRect, protocol.ToolEllipse:
	default:
		return &ValidationError{Msg: fmt.Sprintf("ابزار نامعتبر: %s", req.Tool)}
	}

	req.Color = strings.TrimSpace(req.Color)
	if len([]rune(req.Color)) > maxColorLen {
		return &ValidationError{Msg: "مقدار رنگ بیش از حد بلند است"}
	}
	for _, r := range req.Color {
		if r < 0x20 || r == 0x7f {
			return &ValidationError{Msg: "مقدار رنگ نامعتبر است"}
		}
	}

	// Widths are in the same normalized space as the points.
	if !finite(req.Width) || req.Width <= 0 {
		req.Width = 0.004
	}
	if req.Width > 0.5 {
		req.Width = 0.5
	}
	return nil
}
