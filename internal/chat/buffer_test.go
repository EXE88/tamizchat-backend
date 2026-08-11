package chat

import (
	"errors"
	"testing"

	"tamizchat/internal/protocol"
)

func newTestBuffer(limit int) *Buffer {
	return NewBuffer("room-1", func() int { return limit })
}

func fill(b *Buffer, texts ...string) []protocol.Message {
	out := make([]protocol.Message, 0, len(texts))
	for _, text := range texts {
		out = append(out, b.Append(protocol.Message{
			Kind: protocol.MessageText,
			Text: text,
			Author: protocol.User{
				ClientUUID: "author-1",
				Username:   "Author",
			},
		}))
	}
	return out
}

func TestAppendAssignsIdentityAndOrder(t *testing.T) {
	b := newTestBuffer(10)
	msgs := fill(b, "one", "two")

	if msgs[0].Seq != 1 || msgs[1].Seq != 2 {
		t.Fatalf("sequence numbers should increase: %d, %d", msgs[0].Seq, msgs[1].Seq)
	}
	if msgs[0].ID == "" || msgs[0].ID == msgs[1].ID {
		t.Fatal("every message needs its own id")
	}
	if msgs[0].RoomID != "room-1" {
		t.Fatalf("the buffer should stamp its room id, got %q", msgs[0].RoomID)
	}
}

func TestOldestMessagesAreDropped(t *testing.T) {
	b := newTestBuffer(3)
	fill(b, "a", "b", "c", "d", "e")

	got, hasMore := b.History(0, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	if got[0].Text != "c" || got[2].Text != "e" {
		t.Fatalf("the newest three should be kept, got %+v", got)
	}
	if hasMore {
		t.Fatal("nothing older is still in the buffer")
	}
	// Sequence numbers keep counting even though messages were dropped.
	if got[2].Seq != 5 {
		t.Fatalf("sequence should not restart, got %d", got[2].Seq)
	}
}

func TestHistoryPagesBackwards(t *testing.T) {
	b := newTestBuffer(10)
	msgs := fill(b, "a", "b", "c", "d")

	page, hasMore := b.History(msgs[2].Seq, 2)
	if len(page) != 2 || page[0].Text != "a" || page[1].Text != "b" {
		t.Fatalf("unexpected page: %+v", page)
	}
	if hasMore {
		t.Fatal("nothing older than 'a' exists")
	}

	page, hasMore = b.History(msgs[3].Seq, 1)
	if len(page) != 1 || page[0].Text != "c" {
		t.Fatalf("unexpected page: %+v", page)
	}
	if !hasMore {
		t.Fatal("older messages remain")
	}
}

func TestHistoryOnEmptyBuffer(t *testing.T) {
	b := newTestBuffer(10)
	got, hasMore := b.History(0, 10)
	if len(got) != 0 || hasMore {
		t.Fatalf("an empty buffer should return nothing, got %+v", got)
	}
}

// History must hand out copies: a caller mutating the returned slice must not
// corrupt what the next client reads.
func TestHistoryReturnsCopies(t *testing.T) {
	b := newTestBuffer(10)
	fill(b, "original")

	got, _ := b.History(0, 10)
	got[0].Text = "tampered"

	again, _ := b.History(0, 10)
	if again[0].Text != "original" {
		t.Fatalf("the buffer was modified through a returned slice: %q", again[0].Text)
	}
}

func TestEditRejectionLeavesMessageUntouched(t *testing.T) {
	b := newTestBuffer(10)
	msgs := fill(b, "hello")
	denied := errors.New("denied")

	if _, err := b.Edit(msgs[0].ID, func(*protocol.Message) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("expected the callback's error, got %v", err)
	}

	got, _ := b.History(0, 10)
	if got[0].Text != "hello" {
		t.Fatalf("a rejected edit must not be applied, got %q", got[0].Text)
	}
}

func TestEditAndDeleteReportMissingMessages(t *testing.T) {
	b := newTestBuffer(10)

	if _, err := b.Edit("nope", func(*protocol.Message) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := b.Delete("nope", func(protocol.Message) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteRemovesOnlyTheTarget(t *testing.T) {
	b := newTestBuffer(10)
	msgs := fill(b, "a", "b", "c")

	if _, err := b.Delete(msgs[1].ID, func(protocol.Message) error { return nil }); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, _ := b.History(0, 10)
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "c" {
		t.Fatalf("unexpected buffer after delete: %+v", got)
	}
}

func TestClearEmptiesTheBuffer(t *testing.T) {
	b := newTestBuffer(10)
	fill(b, "a", "b")

	b.Clear()

	if b.Len() != 0 {
		t.Fatalf("expected an empty buffer, got %d messages", b.Len())
	}
	// The buffer stays usable: a room can be re-entered after a purge.
	msg := b.Append(protocol.Message{Kind: protocol.MessageText, Text: "fresh"})
	if msg.Seq != 3 {
		t.Fatalf("sequence should continue after a purge, got %d", msg.Seq)
	}
}
