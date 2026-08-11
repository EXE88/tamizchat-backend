package paint

import (
	"errors"
	"math"
	"testing"

	"tamizchat/internal/protocol"
)

func newTestBoard(limit int) *Board {
	return NewBoard("room-1", func() int { return limit })
}

func begin(t *testing.T, b *Board, author string, points ...protocol.Point) protocol.Stroke {
	t.Helper()
	stroke, err := b.Begin(author, protocol.PaintBegin{Tool: protocol.ToolPen, Points: points})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return stroke
}

func p(x, y float64) protocol.Point { return protocol.Point{X: x, Y: y} }

func TestStrokeLengthIsCapped(t *testing.T) {
	b := newTestBoard(10)
	stroke := begin(t, b, "author")

	// Push well past the per-stroke ceiling in full-size batches.
	batch := make([]protocol.Point, maxPointsPerMessage)
	total := 0
	for i := 0; i < (maxPointsPerStroke/maxPointsPerMessage)+3; i++ {
		stored, err := b.Append("author", stroke.ID, batch)
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		total += len(stored)
	}

	if total != maxPointsPerStroke {
		t.Fatalf("stored %d points, want the cap of %d", total, maxPointsPerStroke)
	}

	// Appending after the cap is not an error — the drawing simply stops
	// growing — but nothing more is stored or echoed to the room.
	stored, err := b.Append("author", stroke.ID, batch)
	if err != nil || len(stored) != 0 {
		t.Fatalf("past the cap: stored %d, err %v", len(stored), err)
	}
}

func TestOversizedBatchIsTrimmed(t *testing.T) {
	b := newTestBoard(10)
	huge := make([]protocol.Point, maxPointsPerMessage*4)
	stroke := begin(t, b, "author", huge...)

	if len(stroke.Points) != maxPointsPerMessage {
		t.Fatalf("a single message should be trimmed to %d points, got %d",
			maxPointsPerMessage, len(stroke.Points))
	}
}

func TestNonFiniteCoordinatesAreDropped(t *testing.T) {
	b := newTestBoard(10)
	stroke := begin(t, b, "author",
		p(0.5, 0.5),
		p(math.NaN(), 0.5),
		p(0.5, math.Inf(1)),
		p(math.Inf(-1), math.Inf(-1)),
		p(0.25, 0.75),
	)

	if len(stroke.Points) != 2 {
		t.Fatalf("only the two usable points should survive, got %+v", stroke.Points)
	}
	if stroke.Points[0] != (protocol.Point{X: 0.5, Y: 0.5}) {
		t.Fatalf("unexpected first point: %+v", stroke.Points[0])
	}
}

func TestBoardRefusesStrokesWhenFull(t *testing.T) {
	b := newTestBoard(2)
	begin(t, b, "author")
	begin(t, b, "author")

	if _, err := b.Begin("author", protocol.PaintBegin{}); !errors.Is(err, ErrFull) {
		t.Fatalf("expected ErrFull, got %v", err)
	}

	// Undoing frees a slot, so a full board is recoverable without wiping it.
	if _, err := b.Undo("author"); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if _, err := b.Begin("author", protocol.PaintBegin{}); err != nil {
		t.Fatalf("after undo the board has room again: %v", err)
	}
}

func TestUndoWalksBackwardsThroughYourOwnStrokes(t *testing.T) {
	b := newTestBoard(10)
	first := begin(t, b, "alice")
	begin(t, b, "bob")
	second := begin(t, b, "alice")

	id, err := b.Undo("alice")
	if err != nil || id != second.ID {
		t.Fatalf("undo should remove alice's newest stroke, got %q (%v)", id, err)
	}
	id, err = b.Undo("alice")
	if err != nil || id != first.ID {
		t.Fatalf("undo should then remove her older one, got %q (%v)", id, err)
	}
	if _, err := b.Undo("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nothing of alice's is left, got %v", err)
	}
	if b.Len() != 1 {
		t.Fatalf("bob's stroke should still be there, board has %d", b.Len())
	}
}

func TestClearAuthorLeavesOtherPeoplesWork(t *testing.T) {
	b := newTestBoard(10)
	begin(t, b, "alice")
	begin(t, b, "bob")
	begin(t, b, "alice")

	if removed := b.ClearAuthor("alice"); removed != 2 {
		t.Fatalf("expected two strokes removed, got %d", removed)
	}
	strokes := b.Snapshot()
	if len(strokes) != 1 || strokes[0].Author != "bob" {
		t.Fatalf("unexpected board: %+v", strokes)
	}
}

// Snapshot must hand out copies: a client's frame is serialized outside the
// board's lock, and must not observe a stroke growing underneath it.
func TestSnapshotIsIndependentOfTheBoard(t *testing.T) {
	b := newTestBoard(10)
	stroke := begin(t, b, "author", p(0.1, 0.1))

	snapshot := b.Snapshot()
	if _, err := b.Append("author", stroke.ID, []protocol.Point{p(0.2, 0.2)}); err != nil {
		t.Fatalf("append: %v", err)
	}

	if len(snapshot[0].Points) != 1 {
		t.Fatalf("the snapshot changed under the caller: %+v", snapshot[0].Points)
	}
	if len(b.Snapshot()[0].Points) != 2 {
		t.Fatal("the board itself should have both points")
	}
}

func TestAppendingToAFinishedStrokeIsRefused(t *testing.T) {
	b := newTestBoard(10)
	stroke := begin(t, b, "author", p(0.1, 0.1))

	if err := b.End("author", stroke.ID); err != nil {
		t.Fatalf("end: %v", err)
	}
	if _, err := b.Append("author", stroke.ID, []protocol.Point{p(0.2, 0.2)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestClearEmptiesTheBoardButKeepsItUsable(t *testing.T) {
	b := newTestBoard(10)
	begin(t, b, "author")
	b.Clear()

	if b.Len() != 0 {
		t.Fatalf("expected an empty board, got %d strokes", b.Len())
	}
	if _, err := b.Begin("author", protocol.PaintBegin{}); err != nil {
		t.Fatalf("a purged room must still be drawable: %v", err)
	}
}
