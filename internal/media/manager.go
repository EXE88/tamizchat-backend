package media

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"tamizchat/internal/authz"
	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
)

// Errors the gateway maps onto protocol error codes.
var (
	ErrDisabled  = errors.New("media is disabled")
	ErrNotInRoom = errors.New("not in a room")
	ErrNoRights  = errors.New("not allowed to join media")
)

// tokenTTL is how long a join token stays valid. It is only needed for the
// moment of connecting, so it can be short.
const tokenTTL = 15 * time.Minute

// Sanctions reports who is silenced; access.Manager satisfies it.
type Sanctions interface {
	IsMuted(clientUUID string) bool
}

// Manager issues LiveKit tokens and enforces moderation on the media side.
type Manager struct {
	cfg       *config.Config
	policy    authz.Policy
	sanctions Sanctions
	api       *client
}

// New builds the media manager. It never contacts LiveKit at startup: a server
// whose media is misconfigured must still run its chat.
func New(cfg *config.Config, policy authz.Policy, sanctions Sanctions) *Manager {
	return &Manager{cfg: cfg, policy: policy, sanctions: sanctions, api: newClient()}
}

// Enabled reports whether media is switched on and configured.
func (m *Manager) Enabled() bool {
	return m.cfg.Bool(config.KeyLiveKitEnabled) &&
		strings.TrimSpace(m.cfg.String(config.KeyLiveKitURL)) != "" &&
		strings.TrimSpace(m.cfg.String(config.KeyLiveKitAPIKey)) != "" &&
		strings.TrimSpace(m.cfg.String(config.KeyLiveKitAPISecret)) != ""
}

// RightsOf works out what a user may publish right now. A server-side mute
// takes the microphone away here, in the token, rather than trusting the client
// to stay quiet.
func (m *Manager) RightsOf(clientUUID string) Rights {
	if m.sanctions.IsMuted(clientUUID) {
		return Rights{}
	}
	return Rights{
		Speak:       m.policy.Can(clientUUID, authz.PermSpeak),
		Video:       m.policy.Can(clientUUID, authz.PermPublishVideo),
		ScreenShare: m.policy.Can(clientUUID, authz.PermShareScreen),
	}
}

// IssueToken returns the credentials for the caller's current room.
func (m *Manager) IssueToken(sess *session.Session) (protocol.MediaToken, error) {
	if !m.Enabled() {
		return protocol.MediaToken{}, ErrDisabled
	}

	roomID := sess.RoomID()
	if roomID == "" {
		return protocol.MediaToken{}, ErrNotInRoom
	}

	rights := m.RightsOf(sess.ClientUUID)
	// Someone who may neither speak nor show anything can still listen and
	// watch, so a listener-only token is issued rather than an error.

	expires := time.Now().Add(tokenTTL)
	token, err := joinToken(
		m.cfg.String(config.KeyLiveKitAPIKey),
		m.cfg.String(config.KeyLiveKitAPISecret),
		roomID, sess.ClientUUID, sess.Username(), rights, tokenTTL)
	if err != nil {
		return protocol.MediaToken{}, err
	}

	return protocol.MediaToken{
		URL:             m.cfg.String(config.KeyLiveKitURL),
		Token:           token,
		Room:            roomID,
		Identity:        sess.ClientUUID,
		ExpiresAt:       expires.Unix(),
		CanSpeak:        rights.Speak,
		CanPublishVideo: rights.Video,
		CanShareScreen:  rights.ScreenShare,
	}, nil
}

// BotCredentials is what a music bot needs to join LiveKit as a participant of
// its own: the address, and a token that may publish audio and nothing else.
type BotCredentials struct {
	URL      string
	Token    string
	Room     string
	Identity string
}

// IssueBotToken authorizes a bot to publish into a room.
//
// A bot is a participant like any other — that is the whole point of the design
// — but it only ever sends: it publishes its music and subscribes to nothing,
// so a room full of people costs it nothing and it can never be made to listen.
func (m *Manager) IssueBotToken(roomID, identity, name string) (BotCredentials, error) {
	if !m.Enabled() {
		return BotCredentials{}, ErrDisabled
	}
	if roomID == "" {
		return BotCredentials{}, ErrNotInRoom
	}

	token, err := botToken(
		m.cfg.String(config.KeyLiveKitAPIKey),
		m.cfg.String(config.KeyLiveKitAPISecret),
		roomID, identity, name, botTokenTTL)
	if err != nil {
		return BotCredentials{}, err
	}

	// The bot runs inside this server, so it reaches LiveKit at the server's own
	// address rather than the one handed to clients.
	url := strings.TrimSpace(m.cfg.String(config.KeyLiveKitAPIURL))
	if url == "" {
		url = m.cfg.String(config.KeyLiveKitURL)
	}

	return BotCredentials{URL: url, Token: token, Room: roomID, Identity: identity}, nil
}

// Disconnect removes a participant from a room's media session. It is called
// whenever someone leaves a room, is moved, kicked or banned: without it a
// kicked user would keep talking to a room they can no longer see.
func (m *Manager) Disconnect(clientUUID, roomID string) {
	if !m.Enabled() || roomID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	if err := m.api.removeParticipant(ctx, m.base(),
		m.cfg.String(config.KeyLiveKitAPIKey),
		m.cfg.String(config.KeyLiveKitAPISecret), roomID, clientUUID); err != nil {
		// A participant who was never in the media session is the normal case:
		// most users are in a text room without their microphone on.
		slog.Debug("livekit remove participant", "room", roomID, "identity", clientUUID, "err", err)
	}
}

// SyncRights pushes a user's current publishing rights to LiveKit. Muting
// someone who is already speaking has to reach the media server, not just the
// next token they ask for.
func (m *Manager) SyncRights(clientUUID, roomID string) {
	if !m.Enabled() || roomID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	if err := m.api.setPermission(ctx, m.base(),
		m.cfg.String(config.KeyLiveKitAPIKey),
		m.cfg.String(config.KeyLiveKitAPISecret),
		roomID, clientUUID, m.RightsOf(clientUUID)); err != nil {
		slog.Debug("livekit update participant", "room", roomID, "identity", clientUUID, "err", err)
	}
}

// CloseRoom deletes the LiveKit room behind a TamizChat room. Called when the
// room is deleted, so nobody is left in a media session for a room that no
// longer exists.
func (m *Manager) CloseRoom(roomID string) {
	if !m.Enabled() || roomID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	if err := m.api.deleteRoom(ctx, m.base(),
		m.cfg.String(config.KeyLiveKitAPIKey),
		m.cfg.String(config.KeyLiveKitAPISecret), roomID); err != nil {
		slog.Debug("livekit delete room", "room", roomID, "err", err)
	}
}

// base is the address *this server* calls LiveKit's API at.
//
// It is a separate setting from the one handed to clients because the two
// audiences do not always agree: a server in a container reaches LiveKit by an
// internal name that means nothing on a user's machine, and a client reaches it
// by a public one the server may not be able to route to. When it is empty —
// the ordinary case — both use the same address.
func (m *Manager) base() string {
	if api := strings.TrimSpace(m.cfg.String(config.KeyLiveKitAPIURL)); api != "" {
		return httpBase(api)
	}
	return httpBase(m.cfg.String(config.KeyLiveKitURL))
}
