// Package paint holds the shared whiteboard of each room.
//
// A board is a list of strokes in the order they were drawn, kept in memory and
// thrown away with the rest of the room's contents. Strokes stream: a client
// opens one, appends points to it while the pointer moves, and closes it — so
// everyone else watches the line appear rather than seeing it pop into
// existence when the pointer is lifted.
package paint

import (
	"errors"
	"sync"

	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
)

// Errors the manager and the gateway map onto protocol codes.
var (
	ErrNotFound = errors.New("stroke not found")
	ErrFull     = errors.New("board is full")
	ErrNotYours = errors.New("stroke belongs to someone else")
)

const (
	// maxPointsPerStroke bounds a single stroke, so one very long drag cannot
	// grow without limit.
	maxPointsPerStroke = 4000
	// maxPointsPerMessage bounds one append; a client sending more is either
	// broken or trying to bypass the rate limit by batching.
	maxPointsPerMessage = 512
)

// Board is one room's whiteboard.
type Board struct {
	roomID string
	limit  func() int

	mu      sync.RWMutex
	strokes []*protocol.Stroke
	index   map[string]*protocol.Stroke
	seq     uint64
}

// NewBoard creates an empty board. limit is read on every stroke, so an admin
// can change the cap without a restart.
func NewBoard(roomID string, limit func() int) *Board {
	return &Board{roomID: roomID, limit: limit, index: make(map[string]*protocol.Stroke)}
}

// Begin opens a stroke and returns it with its assigned id.
func (b *Board) Begin(author string, req protocol.PaintBegin) (protocol.Stroke, error) {
	points := clampPoints(req.Points, maxPointsPerMessage)

	b.mu.Lock()
	defer b.mu.Unlock()

	// A full board refuses new strokes rather than dropping old ones: silently
	// erasing the beginning of a drawing would be worse than saying no.
	if max := b.limit(); max > 0 && len(b.strokes) >= max {
		return protocol.Stroke{}, ErrFull
	}

	b.seq++
	stroke := &protocol.Stroke{
		ID:     storage.NewUUID(),
		Seq:    b.seq,
		RoomID: b.roomID,
		Author: author,
		Tool:   req.Tool,
		Color:  req.Color,
		Width:  req.Width,
		Points: points,
	}
	b.strokes = append(b.strokes, stroke)
	b.index[stroke.ID] = stroke

	return *stroke, nil
}

// Append adds points to an open stroke and returns the points actually stored,
// which may be fewer than asked for if the stroke hit its length limit.
func (b *Board) Append(author, strokeID string, points []protocol.Point) ([]protocol.Point, error) {
	points = clampPoints(points, maxPointsPerMessage)

	b.mu.Lock()
	defer b.mu.Unlock()

	stroke, ok := b.index[strokeID]
	if !ok {
		return nil, ErrNotFound
	}
	if stroke.Author != author {
		return nil, ErrNotYours
	}
	if stroke.Done {
		return nil, ErrNotFound
	}

	room := maxPointsPerStroke - len(stroke.Points)
	if room <= 0 {
		return nil, nil
	}
	if len(points) > room {
		points = points[:room]
	}
	stroke.Points = append(stroke.Points, points...)
	return points, nil
}

// End closes a stroke.
func (b *Board) End(author, strokeID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	stroke, ok := b.index[strokeID]
	if !ok {
		return ErrNotFound
	}
	if stroke.Author != author {
		return ErrNotYours
	}
	stroke.Done = true
	return nil
}

// Undo removes the author's most recent stroke and returns its id.
func (b *Board) Undo(author string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i := len(b.strokes) - 1; i >= 0; i-- {
		if b.strokes[i].Author != author {
			continue
		}
		id := b.strokes[i].ID
		b.strokes = append(b.strokes[:i], b.strokes[i+1:]...)
		delete(b.index, id)
		return id, nil
	}
	return "", ErrNotFound
}

// ClearAll wipes the board.
func (b *Board) ClearAll() {
	b.mu.Lock()
	b.strokes = nil
	b.index = make(map[string]*protocol.Stroke)
	b.mu.Unlock()
}

// ClearAuthor removes every stroke drawn by one person, leaving everyone
// else's work alone.
func (b *Board) ClearAuthor(author string) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	kept := b.strokes[:0]
	removed := 0
	for _, s := range b.strokes {
		if s.Author == author {
			delete(b.index, s.ID)
			removed++
			continue
		}
		kept = append(kept, s)
	}
	b.strokes = kept
	return removed
}

// Snapshot copies the whole board, for a client that just opened it.
func (b *Board) Snapshot() []protocol.Stroke {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]protocol.Stroke, 0, len(b.strokes))
	for _, s := range b.strokes {
		copied := *s
		copied.Points = append([]protocol.Point(nil), s.Points...)
		out = append(out, copied)
	}
	return out
}

// Len is the number of strokes on the board.
func (b *Board) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.strokes)
}

// Clear implements rooms.Ephemeral: the drawing goes when the room empties,
// like everything else that happened inside it.
func (b *Board) Clear() { b.ClearAll() }

// clampPoints trims a batch and drops coordinates that are not finite or are
// far outside the canvas — a stray NaN would corrupt every client's rendering.
func clampPoints(points []protocol.Point, max int) []protocol.Point {
	if len(points) > max {
		points = points[:max]
	}

	out := make([]protocol.Point, 0, len(points))
	for _, p := range points {
		if !finite(p.X) || !finite(p.Y) {
			continue
		}
		out = append(out, protocol.Point{X: clamp01(p.X), Y: clamp01(p.Y)})
	}
	return out
}

// finite rejects NaN and the infinities without importing math for one check.
func finite(v float64) bool { return v == v && v-v == 0 }

// clamp01 keeps a coordinate inside the canvas, with a little slack so a stroke
// that runs off the edge still looks right.
func clamp01(v float64) float64 {
	const slack = 0.5
	switch {
	case v < -slack:
		return -slack
	case v > 1+slack:
		return 1 + slack
	}
	return v
}
