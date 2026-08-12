// Package authz defines what a user is allowed to do.
//
// Permissions are a bitmask carried by roles; a user's effective permissions
// are the union of every role they hold. Nothing in the server checks a user's
// identity directly — every decision goes through Policy, so there is exactly
// one place where the rules live.
package authz

// Permission is a single capability.
type Permission uint64

// The permission set. These values are persisted inside role rows, so existing
// bits must never be renumbered — only new ones appended.
const (
	PermSendMessages Permission = 1 << iota
	PermModerateChat
	PermManageRooms
	PermJoinLockedRooms
	PermBypassRoomPassword
	PermKick
	PermBan
	PermMute
	PermMoveUsers
	PermManageRoles
	PermUploadFiles
	PermControlBots
	PermSpeak
	PermPublishVideo
	PermShareScreen
	PermPaint
)

// Descriptor documents one permission for the admin panel and the client.
type Descriptor struct {
	Perm  Permission
	Key   string // stable identifier used on the wire
	Title string // human-readable label
}

// All lists every permission in the order the panel should show them.
var All = []Descriptor{
	{PermSendMessages, "send_messages", "Send messages"},
	{PermUploadFiles, "upload_files", "Send files and images"},
	{PermModerateChat, "moderate_chat", "Delete other people's messages"},
	{PermManageRooms, "manage_rooms", "Create, edit and delete rooms"},
	{PermJoinLockedRooms, "join_locked_rooms", "Join role-restricted rooms"},
	{PermBypassRoomPassword, "bypass_room_password", "Join a room without its password"},
	{PermKick, "kick", "Kick a user from the server"},
	{PermBan, "ban", "Ban a user"},
	{PermMute, "mute", "Mute a user"},
	{PermMoveUsers, "move_users", "Move users between rooms"},
	{PermSpeak, "speak", "Talk on the microphone"},
	{PermPublishVideo, "publish_video", "Turn on the camera"},
	{PermShareScreen, "share_screen", "Share the screen"},
	{PermPaint, "paint", "Draw on the paint board"},
	{PermManageRoles, "manage_roles", "Manage roles and their assignment"},
	{PermControlBots, "control_bots", "Control bots"},
}

// Lookup finds a permission by its wire key.
func Lookup(key string) (Descriptor, bool) {
	for _, d := range All {
		if d.Key == key {
			return d, true
		}
	}
	return Descriptor{}, false
}

// Keys expands a bitmask into its wire keys, for sending to a client.
func Keys(mask Permission) []string {
	out := make([]string, 0, len(All))
	for _, d := range All {
		if mask&d.Perm != 0 {
			out = append(out, d.Key)
		}
	}
	return out
}

// Mask folds wire keys back into a bitmask, ignoring unknown ones so an older
// server does not choke on a newer client's vocabulary.
func Mask(keys []string) Permission {
	var mask Permission
	for _, key := range keys {
		if d, ok := Lookup(key); ok {
			mask |= d.Perm
		}
	}
	return mask
}

// Everything is the mask with every permission set.
func Everything() Permission {
	var mask Permission
	for _, d := range All {
		mask |= d.Perm
	}
	return mask
}

// Policy answers permission questions about a user, identified by client UUID.
type Policy interface {
	// Can reports whether the user holds a permission.
	Can(clientUUID string, perm Permission) bool
	// Priority is the rank of the user's strongest role. A moderator may only
	// act on users of strictly lower priority, which is what stops two admins
	// from banning each other.
	Priority(clientUUID string) int
}

// OpenPolicy grants everything to everyone. It exists for tests and for the
// degenerate case of a server with no roles configured at all; the real server
// runs on the role system in package access.
type OpenPolicy struct{}

// Can always allows.
func (OpenPolicy) Can(string, Permission) bool { return true }

// Priority is the same for everyone, so nobody can act on anybody.
func (OpenPolicy) Priority(string) int { return 0 }
