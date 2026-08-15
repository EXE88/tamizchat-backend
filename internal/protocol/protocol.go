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

	TypeAdminKick       = "admin.kick"
	TypeAdminBan        = "admin.ban"
	TypeAdminUnban      = "admin.unban"
	TypeAdminMute       = "admin.mute"
	TypeAdminUnmute     = "admin.unmute"
	TypeAdminMove       = "admin.move"
	TypeAdminSanctions  = "admin.sanctions"
	TypeAdminRoleList   = "admin.role.list"
	TypeAdminRoleCreate = "admin.role.create"
	TypeAdminRoleUpdate = "admin.role.update"
	TypeAdminRoleDelete = "admin.role.delete"
	TypeAdminRoleGrant  = "admin.role.grant"
	TypeAdminRoleRevoke = "admin.role.revoke"

	TypeFileUploadRequest = "file.upload_request"
	TypeFileDownloadToken = "file.download_token"

	TypeMediaToken    = "media.token"
	TypeMediaSetState = "media.set_state"

	TypePaintBegin  = "paint.begin"
	TypePaintAppend = "paint.append"
	TypePaintEnd    = "paint.end"
	TypePaintUndo   = "paint.undo"
	TypePaintClear  = "paint.clear"
	TypePaintState  = "paint.state"

	TypeBotList    = "bot.list"
	TypeBotControl = "bot.control"
	TypeBotMove    = "bot.move"
	TypeBotCreate  = "bot.create"
	TypeBotUpdate  = "bot.update"
	TypeBotDelete  = "bot.delete"
	TypeBotQueue   = "bot.queue"

	TypeBotPlaylistList   = "bot.playlist.list"
	TypeBotPlaylistCreate = "bot.playlist.create"
	TypeBotPlaylistRename = "bot.playlist.rename"
	TypeBotPlaylistDelete = "bot.playlist.delete"
	TypeBotPlaylistSelect = "bot.playlist.select"

	TypeBotTrackUpload = "bot.track.upload_request"
	TypeBotTrackDelete = "bot.track.delete"
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

	TypeUserKicked       = "user.kicked"
	TypeUserBanned       = "user.banned"
	TypeUserMuted        = "user.muted"
	TypeUserUnmuted      = "user.unmuted"
	TypeUserRolesChanged = "user.roles_changed"

	TypeAdminSanctionList = "admin.sanctions"
	TypeAdminRoles        = "admin.role.list"
	TypeAdminRole         = "admin.role" // one role, after create or update
	TypeAdminRoleGone     = "admin.role.deleted"
	TypeAdminOK           = "admin.ok" // an action succeeded and needs no payload

	TypeFileUploadTicket = "file.upload_ticket"
	TypeFileDownload     = "file.download"

	TypeMediaCredentials = "media.token"
	TypeMediaState       = "media.state"

	TypePaintStarted  = "paint.begin"
	TypePaintAppended = "paint.append"
	TypePaintEnded    = "paint.end"
	TypePaintUndone   = "paint.undo"
	TypePaintCleared  = "paint.clear"
	TypePaintSnapshot = "paint.state"

	TypeBots     = "bot.list"
	TypeBotState = "bot.state"
	// TypeBotGone says a bot no longer exists. bot.state covers a bot appearing
	// as well as changing, so a client that meets an id it does not know should
	// add it rather than ignore the frame.
	TypeBotGone       = "bot.removed"
	TypeBotQueueReply = "bot.queue"

	TypeBotPlaylists       = "bot.playlist.list"
	TypeBotPlaylist        = "bot.playlist" // one playlist, after create or rename
	TypeBotPlaylistGone    = "bot.playlist.deleted"
	TypeBotTrackUploadInfo = "bot.track.upload_ticket"

	TypeServerNotice = "server.notice"
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

	ErrBanned        = "banned"
	ErrMuted         = "muted"
	ErrOutranked     = "outranked"
	ErrUserNotFound  = "user_not_found"
	ErrRoleNotFound  = "role_not_found"
	ErrRoleNameTaken = "role_name_taken"
	ErrRoleProtected = "role_protected"
	ErrRoleRequired  = "room_role_required"
	ErrInvalidInput  = "invalid_input"

	ErrUploadsDisabled = "uploads_disabled"
	ErrFileTooLarge    = "file_too_large"
	ErrRoomQuotaFull   = "room_quota_exceeded"
	ErrFileNotFound    = "file_not_found"
	ErrFileInvalid     = "file_invalid"

	ErrMediaDisabled = "media_disabled"

	ErrPaintDisabled = "paint_disabled"
	ErrPaintFull     = "paint_board_full"
	ErrPaintNotFound = "paint_stroke_not_found"
	ErrPaintInvalid  = "paint_invalid"

	ErrBotNotFound = "bot_not_found"
	ErrBotDisabled = "bot_disabled"
	ErrBotEmpty    = "bot_queue_empty"
	ErrBotAction   = "bot_bad_action"
	ErrBotNameUsed = "bot_name_taken"
	ErrBotTooMany  = "bot_limit_reached"

	ErrPlaylistNotFound = "playlist_not_found"
	ErrPlaylistNameUsed = "playlist_name_taken"
	ErrPlaylistTooMany  = "playlist_limit_reached"
	ErrTrackNotAudio    = "track_not_audio"
	ErrTrackNotFound    = "track_not_found"
)

// Reasons a session ends, reported in user.left and in the close frame.
const (
	ReasonClientLeft = "client_left"
	ReasonReplaced   = "replaced_by_new_connection"
	ReasonTimeout    = "timeout"
	ReasonShutdown   = "server_shutdown"
	ReasonSlow       = "slow_consumer"
	ReasonKicked     = "kicked"
	ReasonBanned     = "banned"
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
	// Roles are the ids of the roles this user holds, strongest first. The
	// client looks their names and colours up in the role list from welcome.
	Roles []string `json:"roles,omitempty"`
	Muted bool     `json:"muted,omitempty"`
	// Media is what the user currently has switched on, as reported by their
	// client. Whether they are *speaking* right now is not here: that changes
	// many times a second and LiveKit already tells every client directly.
	Media MediaState `json:"media"`
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
	// RequiredRoleID is empty for an open room; otherwise only holders of that
	// role (or someone allowed to bypass it) may enter.
	RequiredRoleID string `json:"required_role_id,omitempty"`
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
	Name           string `json:"name"`
	Password       string `json:"password,omitempty"`
	Capacity       int    `json:"capacity,omitempty"`
	RequiredRoleID string `json:"required_role_id,omitempty"`
}

// RoomUpdate edits a room. Only the fields that are present are changed, which
// is why every field is a pointer.
type RoomUpdate struct {
	RoomID         string  `json:"room_id"`
	Name           *string `json:"name,omitempty"`
	Password       *string `json:"password,omitempty"`
	Capacity       *int    `json:"capacity,omitempty"`
	Position       *int    `json:"position,omitempty"`
	RequiredRoleID *string `json:"required_role_id,omitempty"`
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
	ReasonMoved           = "moved_by_admin"
)

// Role is the public view of a role. Permissions travel as stable string keys
// rather than a raw bitmask, so a client built against an older server still
// understands the ones it knows.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	Priority    int      `json:"priority"`
	Color       string   `json:"color,omitempty"`
	// TagStyle is opaque JSON the client uses to draw this role's tag. The
	// server stores and echoes it without interpreting it, so new visual
	// options need no protocol change.
	TagStyle  string `json:"tag_style,omitempty"`
	IsDefault bool   `json:"is_default"`
}

// RoleSpec is the create/update payload. Absent fields are left unchanged.
type RoleSpec struct {
	RoleID      string    `json:"role_id,omitempty"`
	Name        *string   `json:"name,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
	Priority    *int      `json:"priority,omitempty"`
	Color       *string   `json:"color,omitempty"`
	TagStyle    *string   `json:"tag_style,omitempty"`
}

// RoleRef names a role in delete requests.
type RoleRef struct {
	RoleID string `json:"role_id"`
}

// RoleAssignment grants or revokes a role.
type RoleAssignment struct {
	ClientUUID string `json:"client_uuid"`
	RoleID     string `json:"role_id"`
}

// UserRoles announces that someone's roles changed.
type UserRoles struct {
	ClientUUID  string   `json:"client_uuid"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// AdminTarget is the common shape of a moderation request. DurationSec of zero
// means permanent for bans and mutes.
type AdminTarget struct {
	ClientUUID  string `json:"client_uuid"`
	Reason      string `json:"reason,omitempty"`
	DurationSec int64  `json:"duration_sec,omitempty"`
}

// AdminMove sends a user into another room, or out of every room when RoomID
// is empty.
type AdminMove struct {
	ClientUUID string `json:"client_uuid"`
	RoomID     string `json:"room_id"`
}

// Sanction is an active ban or mute as shown to an admin client.
type Sanction struct {
	ClientUUID string `json:"client_uuid"`
	Username   string `json:"username,omitempty"`
	Kind       string `json:"kind"` // "ban" or "mute"
	Reason     string `json:"reason,omitempty"`
	CreatedBy  string `json:"created_by,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"` // 0 means permanent
}

// SanctionList is the reply to admin.sanctions.
type SanctionList struct {
	Sanctions []Sanction `json:"sanctions"`
}

// Moderation announces a moderation action to everyone.
type Moderation struct {
	ClientUUID string `json:"client_uuid"`
	Username   string `json:"username,omitempty"`
	ByUUID     string `json:"by_uuid,omitempty"`
	ByUsername string `json:"by_username,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
}

// Message kinds.
const (
	MessageText    = "text"
	MessageSticker = "sticker"
	MessageFile    = "file"
)

// Attachment kinds.
const (
	AttachmentImage = "image"
	AttachmentFile  = "file"
)

// Attachment is a file shared in a room. Like everything else inside a room it
// is temporary: the bytes are deleted when the room empties, and the id stops
// resolving.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	// MIME is what the server detected from the bytes, not what the client
	// claimed.
	MIME string `json:"mime"`
	Kind string `json:"kind"` // "image" or "file"
	// Width, Height and HasThumb are set for images only.
	Width    int  `json:"width,omitempty"`
	Height   int  `json:"height,omitempty"`
	HasThumb bool `json:"has_thumb,omitempty"`
}

// FileUploadRequest asks permission to upload, before sending any bytes. The
// server checks the size, the quota and the permission first, so a client is
// never left having pushed megabytes only to be refused.
type FileUploadRequest struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// FileUploadTicket is the permission to upload exactly one file.
type FileUploadTicket struct {
	UploadID  string `json:"upload_id"`
	URL       string `json:"url"`   // path to POST the bytes to
	Token     string `json:"token"` // also accepted as a bearer token
	ExpiresAt int64  `json:"expires_at"`
	MaxSize   int64  `json:"max_size"`
}

// FileDownloadRequest asks for a link to a file already shared in the room.
type FileDownloadRequest struct {
	FileID string `json:"file_id"`
}

// Bot playback states.
const (
	BotIdle    = "idle"    // not in any room
	BotStopped = "stopped" // in a room, not playing
	BotPlaying = "playing"
)

// Bot control actions.
const (
	BotActionPlay   = "play"
	BotActionStop   = "stop"
	BotActionNext   = "next"
	BotActionPrev   = "prev"
	BotActionSelect = "select"
)

// BotTrack is one entry of a bot's queue. Only the title travels: the path on
// the server is nobody's business.
type BotTrack struct {
	Index int    `json:"index"`
	Title string `json:"title"`
}

// Bot is the public view of a bot: its identity plus what it is doing now.
type Bot struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Color  string `json:"color,omitempty"`
	RoomID string `json:"room_id"`
	State  string `json:"state"`
	// Track is what is playing, or the track that would play next.
	Track      *BotTrack `json:"track,omitempty"`
	TrackCount int       `json:"track_count"`
	// PlaylistID is what the bot plays from, empty for its own folder.
	PlaylistID   string `json:"playlist_id,omitempty"`
	PlaylistName string `json:"playlist_name,omitempty"`
	Loop         bool   `json:"loop"`
	Shuffle      bool   `json:"shuffle"`
	Enabled      bool   `json:"enabled"`
}

// ServerNotice is a message from the server operator to everyone connected.
// It is not a chat message: it belongs to no room and is never stored.
type ServerNotice struct {
	Text string `json:"text"`
	From string `json:"from,omitempty"`
}

// BotList is the reply to bot.list.
type BotList struct {
	Bots []Bot `json:"bots"`
}

// BotControl drives playback. TrackIndex is only read for the "select" action.
type BotControl struct {
	BotID      string `json:"bot_id"`
	Action     string `json:"action"`
	TrackIndex int    `json:"track_index,omitempty"`
}

// BotMove sends a bot to a room, or out of every room when RoomID is empty.
type BotMove struct {
	BotID  string `json:"bot_id"`
	RoomID string `json:"room_id"`
}

// BotSpec creates or edits a bot. Every field is optional on an update, where
// nil means "leave this alone"; a create needs at least a name.
//
// There is deliberately no folder here. The path a bot plays from is a path on
// the server's disk, and a remote client has no business naming one — a bot
// created from a client is given a folder of its own under "bots.dir", and the
// music goes into it by upload. A bot created from the CLI panel keeps pointing
// wherever the operator pointed it.
type BotSpec struct {
	BotID   string  `json:"bot_id,omitempty"` // ignored on create
	Name    *string `json:"name,omitempty"`
	Color   *string `json:"color,omitempty"`
	Loop    *bool   `json:"loop,omitempty"`
	Shuffle *bool   `json:"shuffle,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

// BotRef names a bot in delete requests and in the bot.removed event.
type BotRef struct {
	BotID string `json:"bot_id"`
}

// BotQueue is the full track list, for the panel a client shows on a bot.
type BotQueue struct {
	BotID  string     `json:"bot_id"`
	Tracks []BotTrack `json:"tracks"`
}

// BotRequest names a bot in requests that carry nothing else.
type BotRequest struct {
	BotID string `json:"bot_id"`
}

// BotPlaylist is one named group of tracks belonging to a bot.
type BotPlaylist struct {
	ID         string `json:"id"`
	BotID      string `json:"bot_id"`
	Name       string `json:"name"`
	TrackCount int    `json:"track_count"`
}

// BotPlaylistList is the reply to bot.playlist.list. Active is the playlist the
// bot plays from; empty means it plays its own folder, which is what a bot
// configured from the CLI panel does.
type BotPlaylistList struct {
	BotID     string        `json:"bot_id"`
	Playlists []BotPlaylist `json:"playlists"`
	Active    string        `json:"active_playlist_id"`
}

// BotPlaylistSpec creates, renames, deletes or selects a playlist. Which
// fields matter depends on the message; an empty PlaylistID on select means
// "back to the bot's own folder".
type BotPlaylistSpec struct {
	BotID      string `json:"bot_id"`
	PlaylistID string `json:"playlist_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// BotTrackUploadRequest asks permission to add one track to a playlist.
type BotTrackUploadRequest struct {
	BotID      string `json:"bot_id"`
	PlaylistID string `json:"playlist_id"`
	Name       string `json:"name"`
	Size       int64  `json:"size,omitempty"`
}

// BotTrackUploadTicket is single-use permission to POST the bytes of one track.
type BotTrackUploadTicket struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	MaxSize   int64  `json:"max_size"`
}

// BotTrackRef names one track inside a playlist by its position in the queue.
type BotTrackRef struct {
	BotID      string `json:"bot_id"`
	PlaylistID string `json:"playlist_id"`
	Index      int    `json:"index"`
}

// Paint tools.
const (
	ToolPen     = "pen"
	ToolEraser  = "eraser"
	ToolLine    = "line"
	ToolRect    = "rect"
	ToolEllipse = "ellipse"
)

// Point is a position on the board in normalized coordinates: 0..1 on both
// axes, so a drawing looks the same on every window size.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Stroke is one continuous mark on the board.
type Stroke struct {
	ID     string  `json:"id"`
	Seq    uint64  `json:"seq"`
	RoomID string  `json:"room_id,omitempty"`
	Author string  `json:"author"` // client uuid
	Tool   string  `json:"tool"`
	Color  string  `json:"color"`
	Width  float64 `json:"width"`
	Points []Point `json:"points"`
	Done   bool    `json:"done"`
}

// PaintBegin starts a stroke. The first points may come with it, so a quick
// flick of the mouse is not split into two round trips.
type PaintBegin struct {
	Tool   string  `json:"tool"`
	Color  string  `json:"color"`
	Width  float64 `json:"width"`
	Points []Point `json:"points,omitempty"`
}

// PaintAppend adds points to a stroke that is still being drawn.
type PaintAppend struct {
	StrokeID string  `json:"stroke_id"`
	Points   []Point `json:"points"`
}

// PaintEnd finishes a stroke.
type PaintEnd struct {
	StrokeID string `json:"stroke_id"`
}

// PaintUndo removes the caller's most recent stroke.
type PaintUndo struct {
	StrokeID string `json:"stroke_id"`
	RoomID   string `json:"room_id,omitempty"`
}

// Paint clear scopes.
const (
	ClearMine = "mine" // only the caller's own strokes
	ClearAll  = "all"  // the whole board; needs moderation rights
)

// PaintClear wipes strokes off the board.
type PaintClear struct {
	Scope string `json:"scope"`
}

// PaintCleared announces a wipe.
type PaintCleared struct {
	RoomID string `json:"room_id"`
	Scope  string `json:"scope"`
	By     string `json:"by"`
}

// PaintState is the whole board, sent to a client that just opened it.
type PaintState struct {
	RoomID     string   `json:"room_id"`
	Strokes    []Stroke `json:"strokes"`
	MaxStrokes int      `json:"max_strokes"`
}

// MediaToken is everything a client needs to join the room's LiveKit session.
// The can_* flags mirror what the token actually grants, so the client can grey
// out a control instead of trying and being refused by LiveKit.
type MediaToken struct {
	URL             string `json:"url"`
	Token           string `json:"token"`
	Room            string `json:"room"`
	Identity        string `json:"identity"`
	ExpiresAt       int64  `json:"expires_at"`
	CanSpeak        bool   `json:"can_speak"`
	CanPublishVideo bool   `json:"can_publish_video"`
	CanShareScreen  bool   `json:"can_share_screen"`
}

// MediaState is what a user currently has switched on. It is reported by the
// client, because only the client knows whether its camera is actually on;
// what a user is *allowed* to switch on is decided by the server and enforced
// by LiveKit.
type MediaState struct {
	Mic    bool `json:"mic"`
	Cam    bool `json:"cam"`
	Screen bool `json:"screen"`
}

// MediaStateEvent announces someone's media state to the server.
type MediaStateEvent struct {
	ClientUUID string     `json:"client_uuid"`
	RoomID     string     `json:"room_id"`
	State      MediaState `json:"state"`
}

// FileDownload is a short-lived link. ThumbURL is empty for non-images.
type FileDownload struct {
	FileID    string `json:"file_id"`
	URL       string `json:"url"`
	ThumbURL  string `json:"thumb_url,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}

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
	// Attachment is set on messages of kind "file".
	Attachment *Attachment `json:"attachment,omitempty"`
	CreatedAt  int64       `json:"created_at"`
	EditedAt   int64       `json:"edited_at,omitempty"`
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
	Roles      []Role     `json:"roles"`
	Bots       []Bot      `json:"bots"`
	Limits     UserLimits `json:"limits"`
	// Permissions is what *you* may do, expanded so the client can hide the
	// controls you cannot use. The server checks again on every request.
	Permissions []string `json:"permissions"`
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
	MediaEnabled    bool `json:"media_enabled"`
	PaintEnabled    bool `json:"paint_enabled"`
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
