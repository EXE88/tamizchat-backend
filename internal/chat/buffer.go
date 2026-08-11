// Package chat holds the text conversation of each room.
//
// Nothing here is written to disk. A room's messages live in a bounded buffer
// in memory and are dropped when the room empties — that is the whole point of
// the design, not a limitation to be worked around later.
package chat

import (
	"errors"
	"sync"

	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
)

// ErrNotFound is returned when a message id is not in the buffer — either it
// never existed, or it has already scrolled out of the history limit.
var ErrNotFound = errors.New("message not found")

// Buffer is one room's message history. It keeps at most limit messages and
// drops the oldest as new ones arrive.
type Buffer struct {
	roomID string
	limit  func() int

	mu       sync.RWMutex
	messages []protocol.Message
	seq      uint64
}

// NewBuffer creates the history for a room. limit is read on every append, so
// changing the setting takes effect without a restart.
func NewBuffer(roomID string, limit func() int) *Buffer {
	return &Buffer{roomID: roomID, limit: limit}
}

// Append stores a message, assigning it an id and a sequence number.
func (b *Buffer) Append(msg protocol.Message) protocol.Message {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.seq++
	msg.Seq = b.seq
	msg.ID = storage.NewUUID()
	msg.RoomID = b.roomID
	b.messages = append(b.messages, msg)

	if max := b.limit(); max > 0 && len(b.messages) > max {
		// Drop the oldest messages, and let the backing array shrink with them
		// rather than growing forever behind the slice header.
		drop := len(b.messages) - max
		kept := make([]protocol.Message, max)
		copy(kept, b.messages[drop:])
		b.messages = kept
	}
	return msg
}

// Edit applies fn to the stored message under the lock, so a permission check
// and the change itself cannot race with a concurrent delete.
func (b *Buffer) Edit(id string, fn func(*protocol.Message) error) (protocol.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i := range b.messages {
		if b.messages[i].ID != id {
			continue
		}
		candidate := b.messages[i]
		if err := fn(&candidate); err != nil {
			return protocol.Message{}, err
		}
		b.messages[i] = candidate
		return candidate, nil
	}
	return protocol.Message{}, ErrNotFound
}

// Delete removes a message if fn approves it. The message is dropped outright
// rather than tombstoned: this history is temporary anyway.
func (b *Buffer) Delete(id string, fn func(protocol.Message) error) (protocol.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, msg := range b.messages {
		if msg.ID != id {
			continue
		}
		if err := fn(msg); err != nil {
			return protocol.Message{}, err
		}
		b.messages = append(b.messages[:i], b.messages[i+1:]...)
		return msg, nil
	}
	return protocol.Message{}, ErrNotFound
}

// History returns up to limit messages, oldest first. beforeSeq pages
// backwards: pass the Seq of the oldest message already held to get the ones
// before it. hasMore reports whether older messages remain in the buffer.
func (b *Buffer) History(beforeSeq uint64, limit int) (msgs []protocol.Message, hasMore bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	end := len(b.messages)
	if beforeSeq > 0 {
		end = 0
		for i, m := range b.messages {
			if m.Seq >= beforeSeq {
				break
			}
			end = i + 1
		}
	}

	start := end - limit
	if start < 0 {
		start = 0
	}

	out := make([]protocol.Message, end-start)
	copy(out, b.messages[start:end])
	return out, start > 0
}

// Len is the number of messages currently held.
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.messages)
}

// Clear implements rooms.Ephemeral: it is what makes a room's chat disappear
// once the last member has left.
func (b *Buffer) Clear() {
	b.mu.Lock()
	b.messages = nil
	b.mu.Unlock()
}
