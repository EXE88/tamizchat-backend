// Package authz answers "is this user allowed to do that?".
//
// Phase 5 replaces the implementation here with real roles, permissions and an
// admin list. Until then everything funnels through this one interface, so the
// switch is a single wiring change rather than a hunt through the handlers.
package authz

// Policy decides what a user may do. Users are identified by their client UUID.
type Policy interface {
	// CanManageRooms covers creating, editing and deleting rooms.
	CanManageRooms(clientUUID string) bool
	// CanModerateChat covers deleting other people's messages. Note that it
	// does not cover editing them: nobody may put words in another user's
	// mouth, moderator or not.
	CanModerateChat(clientUUID string) bool
}

// OpenPolicy lets every connected user do everything.
//
// This is deliberate for phase 3: there is no admin list yet, and a server with
// no way to create a room would be untestable. It is NOT a safe default for a
// public deployment — until phase 5 lands, run with a server password.
type OpenPolicy struct{}

// CanManageRooms always allows.
func (OpenPolicy) CanManageRooms(string) bool { return true }

// CanModerateChat always allows.
func (OpenPolicy) CanModerateChat(string) bool { return true }
