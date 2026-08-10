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
)

// Frame types sent by the server.
const (
	TypeWelcome     = "welcome"
	TypePong        = "pong"
	TypeError       = "error"
	TypeUserJoined  = "user.joined"
	TypeUserLeft    = "user.left"
	TypeUserUpdated = "user.updated"
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

// User is the public view of a connected participant.
type User struct {
	ClientUUID string `json:"client_uuid"`
	Username   string `json:"username"`
	JoinedAt   int64  `json:"joined_at"` // unix seconds
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
	Limits     UserLimits `json:"limits"`
}

// UserLimits tells the client what the server will accept, so it can validate
// input before sending it.
type UserLimits struct {
	UsernameMin int `json:"username_min"`
	UsernameMax int `json:"username_max"`
	MaxUsers    int `json:"max_users"`
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
