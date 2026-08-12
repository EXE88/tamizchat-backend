package gateway_test

import (
	"context"
	"strings"
	"testing"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
)

// inRoom connects a client and puts it inside a room, draining the frames the
// other clients' arrival produced.
func (f *fixture) inRoom(t *testing.T, uuid, name, roomID string) *client {
	t.Helper()
	c, _ := f.hello(t, uuid, name, "")
	c.joinRoom(roomID, "")
	return c
}

func (c *client) sendText(text string) protocol.Message {
	c.t.Helper()
	c.send(protocol.TypeChatSend, "m1", protocol.ChatSend{Text: text})
	var msg protocol.Message
	c.decode(c.expect(protocol.TypeChatMessage), &msg)
	return msg
}

func TestChatMessageReachesTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	sent := alice.sendText("  hello world  ")
	if sent.Text != "hello world" {
		t.Fatalf("message should be trimmed, got %q", sent.Text)
	}
	if sent.Kind != protocol.MessageText || sent.RoomID != room.ID || sent.Seq != 1 {
		t.Fatalf("unexpected message: %+v", sent)
	}
	if sent.Author.ClientUUID != uuidA || sent.Author.Username != "Alice" {
		t.Fatalf("unexpected author: %+v", sent.Author)
	}

	var received protocol.Message
	bob.decode(bob.expect(protocol.TypeChatMessage), &received)
	if received.ID != sent.ID || received.Text != "hello world" {
		t.Fatalf("Bob received a different message: %+v", received)
	}
}

func TestChatRequiresBeingInARoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeChatSend, "m1", protocol.ChatSend{Text: "hello"})
	alice.expectError(protocol.ErrNotInRoom)
}

func TestChatIsScopedToTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	first := alice.createRoom("First", "", 0)
	second := alice.createRoom("Second", "", 0)

	// Bob connects after the rooms exist, so they arrive in his welcome rather
	// than as room.created events.
	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)

	alice.joinRoom(first.ID, "")
	bob.expect(protocol.TypeRoomMemberJoined)
	bob.joinRoom(second.ID, "")
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.sendText("only for the first room")

	// Bob is elsewhere: the next thing he hears must not be that message.
	bob.send(protocol.TypePing, "p1", nil)
	if env := bob.expect(protocol.TypePong); env.ID != "p1" {
		t.Fatalf("unexpected frame id %q", env.ID)
	}
}

func TestEmptyAndOversizedMessagesAreRejected(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyChatMaxMessageLen, "10"); err != nil {
		t.Fatalf("set max len: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	for _, bad := range []string{"", "   \n  ", strings.Repeat("x", 11)} {
		alice.send(protocol.TypeChatSend, "m", protocol.ChatSend{Text: bad})
		alice.expectError(protocol.ErrMessageInvalid)
	}

	// Exactly at the limit is fine.
	if msg := alice.sendText(strings.Repeat("x", 10)); len(msg.Text) != 10 {
		t.Fatalf("a message at the limit should be accepted, got %q", msg.Text)
	}
}

func TestNewlinesSurviveButControlCharactersDoNot(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	msg := alice.sendText("first line\r\nsecond line")
	if msg.Text != "first line\nsecond line" {
		t.Fatalf("line endings should be normalized, got %q", msg.Text)
	}

	alice.send(protocol.TypeChatSend, "m", protocol.ChatSend{Text: "bad\x00text"})
	alice.expectError(protocol.ErrMessageInvalid)
}

func TestStickerMessages(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	alice.send(protocol.TypeChatSend, "s1", protocol.ChatSend{StickerID: "pack1:wave"})
	var msg protocol.Message
	alice.decode(alice.expect(protocol.TypeChatMessage), &msg)
	if msg.Kind != protocol.MessageSticker || msg.StickerID != "pack1:wave" {
		t.Fatalf("unexpected sticker message: %+v", msg)
	}

	alice.send(protocol.TypeChatSend, "s2", protocol.ChatSend{StickerID: "../../etc/passwd"})
	alice.expectError(protocol.ErrMessageInvalid)

	if err := f.cfg.Set(context.Background(), config.KeyChatStickersEnabled, "false"); err != nil {
		t.Fatalf("disable stickers: %v", err)
	}
	alice.send(protocol.TypeChatSend, "s3", protocol.ChatSend{StickerID: "pack1:wave"})
	alice.expectError(protocol.ErrStickersDisabled)
}

func TestHistoryIsReturnedAndPaged(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	for i := 0; i < 5; i++ {
		alice.sendText(string(rune('a' + i)))
	}

	alice.send(protocol.TypeChatHistory, "h1", protocol.ChatHistoryRequest{Limit: 2})
	var page protocol.ChatHistory
	alice.decode(alice.expect(protocol.TypeChatHistoryReply), &page)
	if len(page.Messages) != 2 || page.Messages[0].Text != "d" || page.Messages[1].Text != "e" {
		t.Fatalf("expected the two newest messages in order, got %+v", page.Messages)
	}
	if !page.HasMore {
		t.Fatal("older messages remain, has_more should be true")
	}

	alice.send(protocol.TypeChatHistory, "h2", protocol.ChatHistoryRequest{
		BeforeSeq: page.Messages[0].Seq, Limit: 10,
	})
	var older protocol.ChatHistory
	alice.decode(alice.expect(protocol.TypeChatHistoryReply), &older)
	if len(older.Messages) != 3 || older.Messages[0].Text != "a" {
		t.Fatalf("unexpected older page: %+v", older.Messages)
	}
	if older.HasMore {
		t.Fatal("that was the whole buffer, has_more should be false")
	}
}

func TestHistoryIsBoundedByTheConfiguredLimit(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyRoomsHistoryLimit, "10"); err != nil {
		t.Fatalf("set history limit: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	for i := 0; i < 15; i++ {
		alice.sendText(strings.Repeat("m", i+1))
	}

	alice.send(protocol.TypeChatHistory, "h1", protocol.ChatHistoryRequest{Limit: 100})
	var page protocol.ChatHistory
	alice.decode(alice.expect(protocol.TypeChatHistoryReply), &page)
	if len(page.Messages) != 10 {
		t.Fatalf("the buffer should hold only 10 messages, got %d", len(page.Messages))
	}
	if page.Messages[0].Text != strings.Repeat("m", 6) {
		t.Fatalf("the oldest five should have been dropped, got %q", page.Messages[0].Text)
	}
}

func TestEditIsAuthorOnly(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	sent := alice.sendText("frist")
	bob.expect(protocol.TypeChatMessage)

	alice.send(protocol.TypeChatEdit, "e1", protocol.ChatEdit{MessageID: sent.ID, Text: "first"})
	var edited protocol.Message
	alice.decode(alice.expect(protocol.TypeChatUpdated), &edited)
	if edited.Text != "first" || edited.EditedAt == 0 {
		t.Fatalf("unexpected edit result: %+v", edited)
	}

	var seen protocol.Message
	bob.decode(bob.expect(protocol.TypeChatUpdated), &seen)
	if seen.Text != "first" {
		t.Fatalf("Bob should see the edit, got %q", seen.Text)
	}

	// Even with full moderation rights, nobody may rewrite someone else's words.
	bob.send(protocol.TypeChatEdit, "e2", protocol.ChatEdit{MessageID: sent.ID, Text: "hacked"})
	bob.expectError(protocol.ErrForbidden)
}

func TestAuthorCanDeleteOwnMessage(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	sent := alice.sendText("oops")
	bob.expect(protocol.TypeChatMessage)

	alice.send(protocol.TypeChatDelete, "d1", protocol.ChatDelete{MessageID: sent.ID})
	var deleted protocol.ChatDeleted
	alice.decode(alice.expect(protocol.TypeChatDeleted), &deleted)
	if deleted.MessageID != sent.ID || deleted.DeletedBy != "" {
		t.Fatalf("unexpected deletion: %+v", deleted)
	}
	bob.expect(protocol.TypeChatDeleted)

	// It is gone from the history, not tombstoned.
	alice.send(protocol.TypeChatHistory, "h1", nil)
	var page protocol.ChatHistory
	alice.decode(alice.expect(protocol.TypeChatHistoryReply), &page)
	if len(page.Messages) != 0 {
		t.Fatalf("the message should be gone, got %+v", page.Messages)
	}

	alice.send(protocol.TypeChatDelete, "d2", protocol.ChatDelete{MessageID: sent.ID})
	alice.expectError(protocol.ErrMessageNotFound)
}

func TestModeratorCanDeleteOthersMessages(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	sent := bob.sendText("spam")
	alice.expect(protocol.TypeChatMessage)

	alice.send(protocol.TypeChatDelete, "d1", protocol.ChatDelete{MessageID: sent.ID})
	var deleted protocol.ChatDeleted
	alice.decode(alice.expect(protocol.TypeChatDeleted), &deleted)
	if deleted.DeletedBy != uuidA {
		t.Fatalf("a moderator deletion should name the moderator, got %+v", deleted)
	}
	bob.expect(protocol.TypeChatDeleted)

	// The other way round is refused: Bob holds only the default role.
	mine := alice.sendText("mine")
	bob.expect(protocol.TypeChatMessage)
	bob.send(protocol.TypeChatDelete, "d2", protocol.ChatDelete{MessageID: mine.ID})
	bob.expectError(protocol.ErrForbidden)
}

func TestTypingIsForwardedToTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	alice.send(protocol.TypeChatTyping, "t1", protocol.ChatTyping{Typing: true})

	var typing protocol.ChatTyping
	bob.decode(bob.expect(protocol.TypeChatTypingEvent), &typing)
	if !typing.Typing || typing.ClientUUID != uuidA || typing.Username != "Alice" {
		t.Fatalf("unexpected typing event: %+v", typing)
	}
	if typing.RoomID != room.ID {
		t.Fatalf("typing event should name the room, got %q", typing.RoomID)
	}

	// A typing hint outside a room is silently ignored rather than answered
	// with an error the user cannot act on.
	loner, _ := f.hello(t, uuidC, "Loner", "")
	loner.send(protocol.TypeChatTyping, "t2", protocol.ChatTyping{Typing: true})
	loner.send(protocol.TypePing, "p1", nil)
	loner.expect(protocol.TypePong)
}

func TestSendingIsRateLimited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.cfg.Set(ctx, config.KeyChatRateBurst, "3"); err != nil {
		t.Fatalf("set burst: %v", err)
	}
	// One message per minute sustained: the burst is all the client gets
	// within the lifetime of this test.
	if err := f.cfg.Set(ctx, config.KeyChatRatePerMinute, "1"); err != nil {
		t.Fatalf("set rate: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	for i := 0; i < 3; i++ {
		alice.sendText("burst")
	}

	alice.send(protocol.TypeChatSend, "m4", protocol.ChatSend{Text: "one too many"})
	alice.expectError(protocol.ErrTooFast)
}

// A rejected message must not cost the sender a token, or a client typing an
// over-long message would be silenced for spamming.
func TestRejectedMessagesDoNotConsumeTheRateBudget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.cfg.Set(ctx, config.KeyChatRateBurst, "2"); err != nil {
		t.Fatalf("set burst: %v", err)
	}
	if err := f.cfg.Set(ctx, config.KeyChatRatePerMinute, "1"); err != nil {
		t.Fatalf("set rate: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	for i := 0; i < 5; i++ {
		alice.send(protocol.TypeChatSend, "bad", protocol.ChatSend{Text: "  "})
		alice.expectError(protocol.ErrMessageInvalid)
	}

	alice.sendText("first")
	alice.sendText("second")
}

// Leaving a room empty must wipe its chat, and re-entering must start clean.
func TestChatIsPurgedWithTheRoom(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyRoomsPurgeGraceSec, "0"); err != nil {
		t.Fatalf("set grace: %v", err)
	}

	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")
	alice.sendText("this must not survive")

	alice.send(protocol.TypeRoomLeave, "l1", nil)
	alice.expectFrames(protocol.TypeRoomLeft, protocol.TypeRoomPurged)

	alice.joinRoom(room.ID, "")
	alice.send(protocol.TypeChatHistory, "h1", nil)
	var page protocol.ChatHistory
	alice.decode(alice.expect(protocol.TypeChatHistoryReply), &page)
	if len(page.Messages) != 0 {
		t.Fatalf("the room's chat should have been dropped, got %+v", page.Messages)
	}
}

func TestEditingAStickerIsRejected(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")

	alice.send(protocol.TypeChatSend, "s1", protocol.ChatSend{StickerID: "pack1:wave"})
	var msg protocol.Message
	alice.decode(alice.expect(protocol.TypeChatMessage), &msg)

	alice.send(protocol.TypeChatEdit, "e1", protocol.ChatEdit{MessageID: msg.ID, Text: "nope"})
	alice.expectError(protocol.ErrMessageInvalid)
}
