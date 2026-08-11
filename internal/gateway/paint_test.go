package gateway_test

import (
	"context"
	"testing"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
)

// draw opens a stroke and returns it as the server stored it.
func (c *client) draw(points ...protocol.Point) protocol.Stroke {
	c.t.Helper()
	c.send(protocol.TypePaintBegin, "p1", protocol.PaintBegin{
		Tool: protocol.ToolPen, Color: "#ff0000", Width: 0.01, Points: points,
	})
	var stroke protocol.Stroke
	c.decode(c.expect(protocol.TypePaintStarted), &stroke)
	return stroke
}

func pt(x, y float64) protocol.Point { return protocol.Point{X: x, Y: y} }

func TestStrokeStreamsToTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	stroke := alice.draw(pt(0.1, 0.1))
	if stroke.ID == "" || stroke.Author != uuidA || stroke.RoomID != room.ID {
		t.Fatalf("unexpected stroke: %+v", stroke)
	}
	if stroke.Tool != protocol.ToolPen || stroke.Color != "#ff0000" {
		t.Fatalf("the stroke should keep its tool and colour: %+v", stroke)
	}

	var seen protocol.Stroke
	bob.decode(bob.expect(protocol.TypePaintStarted), &seen)
	if seen.ID != stroke.ID || len(seen.Points) != 1 {
		t.Fatalf("Bob should see the stroke start: %+v", seen)
	}

	// Points arrive while the line is still being drawn, not after it ends.
	alice.send(protocol.TypePaintAppend, "p2", protocol.PaintAppend{
		StrokeID: stroke.ID, Points: []protocol.Point{pt(0.2, 0.2), pt(0.3, 0.25)},
	})
	var appended protocol.PaintAppend
	bob.decode(bob.expect(protocol.TypePaintAppended), &appended)
	if appended.StrokeID != stroke.ID || len(appended.Points) != 2 {
		t.Fatalf("unexpected append: %+v", appended)
	}

	alice.send(protocol.TypePaintEnd, "p3", protocol.PaintEnd{StrokeID: stroke.ID})
	var ended protocol.PaintEnd
	bob.decode(bob.expect(protocol.TypePaintEnded), &ended)
	if ended.StrokeID != stroke.ID {
		t.Fatalf("unexpected end: %+v", ended)
	}
}

// Someone who walks in later must see what is already on the board.
func TestNewcomerGetsTheWholeBoard(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)

	first := alice.draw(pt(0.1, 0.1))
	alice.send(protocol.TypePaintAppend, "p2", protocol.PaintAppend{
		StrokeID: first.ID, Points: []protocol.Point{pt(0.5, 0.5)},
	})
	alice.send(protocol.TypePaintEnd, "p3", protocol.PaintEnd{StrokeID: first.ID})
	second := alice.draw(pt(0.7, 0.7))

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	bob.send(protocol.TypePaintState, "s1", nil)
	var state protocol.PaintState
	bob.decode(bob.expect(protocol.TypePaintSnapshot), &state)

	if state.RoomID != room.ID || len(state.Strokes) != 2 {
		t.Fatalf("unexpected board: %+v", state)
	}
	if state.Strokes[0].ID != first.ID || state.Strokes[1].ID != second.ID {
		t.Fatal("strokes should come back in the order they were drawn")
	}
	if len(state.Strokes[0].Points) != 2 || !state.Strokes[0].Done {
		t.Fatalf("the finished stroke should carry all its points: %+v", state.Strokes[0])
	}
	if state.Strokes[1].Done {
		t.Fatal("the second stroke is still open")
	}
}

func TestUndoRemovesOnlyYourOwnLastStroke(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	aliceStroke := alice.draw(pt(0.1, 0.1))
	bob.expect(protocol.TypePaintStarted)
	bobStroke := bob.draw(pt(0.2, 0.2))
	alice.expect(protocol.TypePaintStarted)

	// Alice undoes: her own stroke goes, Bob's newer one stays.
	alice.send(protocol.TypePaintUndo, "u1", nil)
	var undone protocol.PaintUndo
	alice.decode(alice.expect(protocol.TypePaintUndone), &undone)
	if undone.StrokeID != aliceStroke.ID {
		t.Fatalf("undo should remove the caller's own stroke, got %+v", undone)
	}
	bob.expect(protocol.TypePaintUndone)

	if got := f.paint.StrokeCount(room.ID); got != 1 {
		t.Fatalf("expected Bob's stroke to survive, board has %d strokes", got)
	}

	// Nothing left of Alice's to undo.
	alice.send(protocol.TypePaintUndo, "u2", nil)
	alice.expectError(protocol.ErrPaintNotFound)
	_ = bobStroke
}

func TestAppendingToSomeoneElsesStrokeIsRefused(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	stroke := alice.draw(pt(0.1, 0.1))
	bob.expect(protocol.TypePaintStarted)

	bob.send(protocol.TypePaintAppend, "p1", protocol.PaintAppend{
		StrokeID: stroke.ID, Points: []protocol.Point{pt(0.9, 0.9)},
	})
	bob.expectError(protocol.ErrPaintNotFound)

	bob.send(protocol.TypePaintEnd, "p2", protocol.PaintEnd{StrokeID: stroke.ID})
	bob.expectError(protocol.ErrPaintNotFound)
}

// Wiping your own work needs nothing special; wiping everyone's destroys other
// people's drawing and needs moderation rights.
func TestClearScopes(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.draw(pt(0.1, 0.1))
	bob.expect(protocol.TypePaintStarted)
	bob.draw(pt(0.2, 0.2))
	alice.expect(protocol.TypePaintStarted)

	// Bob clears his own; Alice's stroke survives.
	bob.send(protocol.TypePaintClear, "c1", protocol.PaintClear{Scope: protocol.ClearMine})
	var cleared protocol.PaintCleared
	bob.decode(bob.expect(protocol.TypePaintCleared), &cleared)
	if cleared.Scope != protocol.ClearMine || cleared.By != uuidB {
		t.Fatalf("unexpected clear event: %+v", cleared)
	}
	alice.expect(protocol.TypePaintCleared)

	if got := f.paint.StrokeCount(room.ID); got != 1 {
		t.Fatalf("clearing your own should leave the others, board has %d", got)
	}

	// Bob may not wipe the whole board: he holds only the default role.
	bob.send(protocol.TypePaintClear, "c2", protocol.PaintClear{Scope: protocol.ClearAll})
	bob.expectError(protocol.ErrForbidden)

	// Alice, an admin, may.
	alice.send(protocol.TypePaintClear, "c3", protocol.PaintClear{Scope: protocol.ClearAll})
	alice.expect(protocol.TypePaintCleared)
	bob.expect(protocol.TypePaintCleared)

	if got := f.paint.StrokeCount(room.ID); got != 0 {
		t.Fatalf("the board should be empty, has %d strokes", got)
	}
}

func TestPaintRequiresPermission(t *testing.T) {
	f := newFixture(t)
	f.defaultRolePermissionsWithout(t, "paint")

	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	bob.send(protocol.TypePaintBegin, "p1", protocol.PaintBegin{Tool: protocol.ToolPen})
	bob.expectError(protocol.ErrForbidden)

	// Reading the board is still fine: they can watch, they just cannot draw.
	bob.send(protocol.TypePaintState, "s1", nil)
	bob.expect(protocol.TypePaintSnapshot)
}

func TestMutedUserCannotDraw(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.send(protocol.TypeAdminMute, "m1", protocol.AdminTarget{ClientUUID: uuidB})
	bob.expect(protocol.TypeUserMuted)
	alice.expect(protocol.TypeAdminOK)

	bob.send(protocol.TypePaintBegin, "p1", protocol.PaintBegin{Tool: protocol.ToolPen})
	bob.expectError(protocol.ErrMuted)
}

func TestPaintRequiresARoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypePaintBegin, "p1", protocol.PaintBegin{Tool: protocol.ToolPen})
	alice.expectError(protocol.ErrNotInRoom)
}

func TestPaintCanBeDisabled(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyPaintEnabled, "false"); err != nil {
		t.Fatalf("disable paint: %v", err)
	}
	alice, _ := f.roomWith(t)

	alice.send(protocol.TypePaintBegin, "p1", protocol.PaintBegin{Tool: protocol.ToolPen})
	alice.expectError(protocol.ErrPaintDisabled)
}

func TestInvalidToolIsRejected(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.roomWith(t)

	alice.send(protocol.TypePaintBegin, "p1", protocol.PaintBegin{Tool: "flamethrower"})
	alice.expectError(protocol.ErrPaintInvalid)
}

// A full board says so rather than silently dropping the oldest strokes, which
// would erase the beginning of whatever was being drawn.
func TestFullBoardRefusesNewStrokes(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyPaintMaxStrokes, "10"); err != nil {
		t.Fatalf("set stroke limit: %v", err)
	}
	alice, _ := f.roomWith(t)

	for i := 0; i < 10; i++ {
		alice.draw(pt(0.1, 0.1))
	}

	alice.send(protocol.TypePaintBegin, "p1", protocol.PaintBegin{Tool: protocol.ToolPen})
	alice.expectError(protocol.ErrPaintFull)
}

func TestDrawingIsRateLimited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.cfg.Set(ctx, config.KeyPaintRateBurst, "3"); err != nil {
		t.Fatalf("set burst: %v", err)
	}
	if err := f.cfg.Set(ctx, config.KeyPaintRatePerSecond, "1"); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	alice, _ := f.roomWith(t)

	for i := 0; i < 3; i++ {
		alice.draw(pt(0.1, 0.1))
	}
	alice.send(protocol.TypePaintBegin, "p1", protocol.PaintBegin{Tool: protocol.ToolPen})
	alice.expectError(protocol.ErrTooFast)
}

// The drawing belongs to the room, and the room's contents are temporary.
func TestBoardIsPurgedWithTheRoom(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyRoomsPurgeGraceSec, "0"); err != nil {
		t.Fatalf("set grace: %v", err)
	}

	alice, room := f.roomWith(t)
	alice.draw(pt(0.1, 0.1))
	if f.paint.StrokeCount(room.ID) != 1 {
		t.Fatal("the stroke should be on the board")
	}

	alice.send(protocol.TypeRoomLeave, "l1", nil)
	alice.expectFrames(protocol.TypeRoomLeft, protocol.TypeRoomPurged)

	if got := f.paint.StrokeCount(room.ID); got != 0 {
		t.Fatalf("the board should have been wiped, has %d strokes", got)
	}

	alice.joinRoom(room.ID, "")
	alice.send(protocol.TypePaintState, "s1", nil)
	var state protocol.PaintState
	alice.decode(alice.expect(protocol.TypePaintSnapshot), &state)
	if len(state.Strokes) != 0 {
		t.Fatalf("a purged room starts with a blank board, got %+v", state.Strokes)
	}
}

// Coordinates come straight off someone else's pointer; a NaN would corrupt
// every other client's canvas.
func TestBrokenCoordinatesAreDropped(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	// JSON has no NaN literal, so a broken client sends it as a huge value.
	stroke := alice.draw(pt(0.5, 0.5), pt(1e30, -1e30))
	bob.expect(protocol.TypePaintStarted)

	if len(stroke.Points) != 2 {
		t.Fatalf("expected both points, got %+v", stroke.Points)
	}
	for _, p := range stroke.Points {
		if p.X < -1 || p.X > 2 || p.Y < -1 || p.Y > 2 {
			t.Fatalf("coordinates should be clamped to the canvas, got %+v", p)
		}
	}
}
