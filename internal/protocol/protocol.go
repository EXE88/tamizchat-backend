// Package protocol defines the wire contract between the client and the server.
//
// Every frame is a JSON object with the same shape:
//
//	{"t": "<type>", "id": "<optional correlation id>", "d": { ... }}
//
// A client request may carry "id"; the reply to it echoes the same "id" so the
// client can match them. Frames the server sends on its own (presence updates,
// chat, …) have no "id".
package protocol

import "encoding/json"

// Version is the wire contract version. Bump it only for breaking changes.
const Version = 1

// Envelope is the outer frame every message shares.
type Envelope struct {
	Type string          `json:"t"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"d,omitempty"`
}

// Frame types sent by the client.
const (
	TypeHello  = "hello"  // first frame after the socket opens
	TypePing   = "ping"   // application-level keepalive
	TypeRename = "rename" // change display name

	TypeRoomList   = "room.list"
	TypeRoomJoin   = "room.join"
	TypeRoomLeave  = "room.leave"
	TypeRoomCreate = "room.create"
	TypeRoomUpdate = "room.update"
	TypeRoomDelete = "room.delete"

	TypeChatSend    = "chat.send"
	TypeChatHistory = "chat.history"
	TypeChatEdit    = "chat.edit"
	TypeChatDelete  = "chat.delete"
	TypeChatTyping  = "chat.typing"
)

// Frame types sent by the server.
const (
	TypeWelcome     = "welcome"
	TypePong        = "pong"
	TypeError       = "error"
	TypeUserJoined  = "user.joined"
	TypeUserLeft    = "user.left"
	TypeUserUpdated = "user.updated"

	TypeRooms            = "room.list"    // reply to room.list
	TypeRoomJoined       = "room.joined"  // reply to room.join, for the caller
	TypeRoomLeft         = "room.left"    // reply to room.leave, for the caller
	TypeRoomCreated      = "room.created" // a room appeared
	TypeRoomUpdated      = "room.updated" // a room definition changed
	TypeRoomDeleted      = "room.deleted" // a room disappeared
	TypeRoomMemberJoined = "room.member_joined"
	TypeRoomMemberLeft   = "room.member_left"
	TypeRoomPurged       = "room.purged" // the room emptied and its content was dropped

	TypeChatMessage      = "chat.message" // a new message (reply to chat.send, or a broadcast)
	TypeChatHistoryReply = "chat.history"
	TypeChatUpdated      = "chat.updated"
	TypeChatDeleted      = "chat.deleted"
	TypeChatTypingEvent  = "chat.typing"
)

// Error codes. The client shows its own localized text per code, so these
// strings are stable identifiers and must not be reworded casually.
const (
	ErrBadRequest      = "bad_request"
	ErrProtocol        = "unsupported_protocol"
	ErrInvalidUUID     = "invalid_client_uuid"
	ErrInvalidUsername = "invalid_username"
	ErrUsernameTaken   = "username_taken"
	ErrBadPassword     = "bad_password"
	ErrServerFull      = "server_full"
	ErrHandshake       = "handshake_required"
	ErrTimeout         = "handshake_timeout"
	ErrTooFast         = "rate_limited"
	ErrInternal        = "internal_error"

	ErrRoomNotFound    = "room_not_found"
	ErrRoomNameTaken   = "room_name_taken"
	ErrRoomPassword    = "room_bad_password"
	ErrRoomFull        = "room_full"
	ErrRoomLimit       = "room_limit_reached"
	ErrRoomInvalidName = "room_invalid_name"
	ErrNotInRoom       = "not_in_a_room"
	ErrForbidden       = "forbidden"

	ErrMessageInvalid   = "message_invalid"
	ErrMessageNotFound  = "message_not_found"
	ErrStickersDisabled = "stickers_disabled"
)

// Reasons a session ends, reported in user.left and in the close frame.
const (
	ReasonClientLeft = "client_left"
	ReasonReplaced   = "replaced_by_new_connection"
	ReasonTimeout    = "timeout"
	ReasonShutdown   = "server_shutdown"
	ReasonSlow       = "slow_consumer"
)

// Hello is the client's opening frame.
type Hello struct {
	ClientUUID    string `json:"client_uuid"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	Protocol      int    `json:"protocol"`
	ClientVersion string `json:"client_version,omitempty"`
}

// User is the public view of a connected participant. RoomID is empty while the
// user is connected to the server but has not entered any room.
type User struct {
	ClientUUID string `json:"client_uuid"`
	Username   string `json:"username"`
	JoinedAt   int64  `json:"joined_at"` // unix seconds
	RoomID     string `json:"room_id"`
}

// Room is the public view of a room. The password itself is never sent; only
// whether one is set.
type Room struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	HasPassword bool   `json:"has_password"`
	Capacity    int    `json:"capacity"`
	Position    int    `json:"position"`
	MemberCount int    `json:"member_count"`
	Members     []User `json:"members"`
}

// RoomList is the reply to room.list.
type RoomList struct {
	Rooms []Room `json:"rooms"`
}

// RoomJoin asks to enter a room.
type RoomJoin struct {
	RoomID   string `json:"room_id"`
	Password string `json:"password,omitempty"`
}

// RoomJoined confirms entry and carries the room's current state.
type RoomJoined struct {
	Room Room `json:"room"`
}

// RoomLeft confirms departure. Reason is empty when the user left on purpose.
type RoomLeft struct {
	RoomID string `json:"room_id"`
	Reason string `json:"reason,omitempty"`
}

// RoomCreate defines a new room. Capacity 0 means "use the server default".
type RoomCreate struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	Capacity int    `json:"capacity,omitempty"`
}

// RoomUpdate edits a room. Only the fields that are present are changed, which
// is why every field is a pointer.
type RoomUpdate struct {
	RoomID   string  `json:"room_id"`
	Name     *string `json:"name,omitempty"`
	Password *string `json:"password,omitempty"`
	Capacity *int    `json:"capacity,omitempty"`
	Position *int    `json:"position,omitempty"`
}

// RoomDelete removes a room.
type RoomDelete struct {
	RoomID string `json:"room_id"`
}

// RoomRef identifies a room in notifications that need nothing else.
type RoomRef struct {
	RoomID string `json:"room_id"`
}

// RoomMember announces someone entering or leaving a room.
type RoomMember struct {
	RoomID string `json:"room_id"`
	User   User   `json:"user"`
	Reason string `json:"reason,omitempty"`
}

// Reasons a user stops being a member of a room.
const (
	ReasonRoomDeleted     = "room_deleted"
	ReasonSwitchedRoom    = "switched_room"
	ReasonDisconnected    = "disconnected"
	ReasonLeftVoluntarily = "left"
)

// Message kinds.
const (
	MessageText    = "text"
	MessageSticker = "sticker"
)

// Message is one chat message. Messages live only in the room's memory: they
// are gone once the room empties, so there is no permanent id to refer to
// later — Seq exists purely for ordering and paging within a live room.
type Message struct {
	ID        string `json:"id"`
	Seq       uint64 `json:"seq"`
	RoomID    string `json:"room_id"`
	Author    User   `json:"author"`
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	StickerID string `json:"sticker_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
	EditedAt  int64  `json:"edited_at,omitempty"`
}

// ChatSend posts a message to the room the sender is currently in. Exactly one
// of Text or StickerID must be set.
type ChatSend struct {
	Text      string `json:"text,omitempty"`
	StickerID string `json:"sticker_id,omitempty"`
}

// ChatHistoryRequest asks for the messages a client missed. BeforeSeq pages
// backwards through the buffer; zero means "from the newest".
type ChatHistoryRequest struct {
	BeforeSeq uint64 `json:"before_seq,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// ChatHistory is the reply, oldest message first.
type ChatHistory struct {
	RoomID   string    `json:"room_id"`
	Messages []Message `json:"messages"`
	// HasMore reports whether older messages are still in the buffer.
	HasMore bool `json:"has_more"`
}

// ChatEdit changes the text of one's own message.
type ChatEdit struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// ChatDelete removes a message.
type ChatDelete struct {
	MessageID string `json:"message_id"`
}

// ChatDeleted announces a removal.
type ChatDeleted struct {
	RoomID    string `json:"room_id"`
	MessageID string `json:"message_id"`
	// DeletedBy is set when a moderator removed someone else's message.
	DeletedBy string `json:"deleted_by,omitempty"`
}

// ChatTyping reports that someone started or stopped typing. It is never
// stored: it is a hint that expires on its own.
type ChatTyping struct {
	RoomID     string `json:"room_id"`
	ClientUUID string `json:"client_uuid,omitempty"`
	Username   string `json:"username,omitempty"`
	Typing     bool   `json:"typing"`
}

// Welcome is the server's answer to a successful hello.
type Welcome struct {
	SessionID  string     `json:"session_id"`
	Protocol   int        `json:"protocol"`
	ServerUUID string     `json:"server_uuid"`
	ServerName string     `json:"server_name"`
	Message    string     `json:"welcome_message"`
	Heartbeat  int        `json:"heartbeat_sec"`
	You        User       `json:"you"`
	Users      []User     `json:"users"`
	Rooms      []Room     `json:"rooms"`
	Limits     UserLimits `json:"limits"`
}

// UserLimits tells the client what the server will accept, so it can validate
// input before sending it.
type UserLimits struct {
	UsernameMin     int  `json:"username_min"`
	UsernameMax     int  `json:"username_max"`
	MaxUsers        int  `json:"max_users"`
	MessageMax      int  `json:"message_max"`
	HistoryLimit    int  `json:"history_limit"`
	StickersEnabled bool `json:"stickers_enabled"`
}

// Error is the payload of an error frame.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Rename asks the server to change the caller's display name.
type Rename struct {
	Username string `json:"username"`
}

// UserLeft announces a departure.
type UserLeft struct {
	ClientUUID string `json:"client_uuid"`
	Username   string `json:"username"`
	Reason     string `json:"reason"`
}

// Encode builds a frame ready to be written to the socket.
func Encode(typ, id string, payload any) ([]byte, error) {
	env := Envelope{Type: typ, ID: id}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		env.Data = raw
	}
	return json.Marshal(env)
}
