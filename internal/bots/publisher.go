package bots

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"tamizchat/internal/media"
)

// ErrNotOpus is returned for a track LiveKit cannot be handed as it is.
var ErrNotOpus = errors.New("a bot can only play Ogg/Opus")

// Publisher is a bot's presence in the media session: it joins a room as a
// participant and publishes its music there.
//
// This is an interface so the tests can run the whole bot engine — queues,
// permissions, moving between rooms — against something that records what it
// was asked to do, instead of a real WebRTC connection.
type Publisher interface {
	// Enabled reports whether media is configured at all.
	Enabled() bool
	// Join puts the bot in a room, silent, as a participant.
	Join(botID, roomID, name string) error
	// Play publishes a file, replacing whatever was playing. onDone is called
	// when the file reaches its end on its own — not when it is replaced or
	// stopped.
	Play(botID, path string, onDone func()) error
	// Stop unpublishes the current track and leaves the bot sitting in the room.
	Stop(botID string)
	// Leave removes the bot from the room entirely.
	Leave(botID string)
}

// livekitPublisher is the real thing: one LiveKit participant per bot.
//
// The audio is published straight from the Ogg file. Nothing decodes or
// re-encodes it, here or anywhere else — an Ogg/Opus file already holds exactly
// the packets WebRTC carries, so the server hands them over and the SDK paces
// them by the timestamps in the file itself. That is what makes this the stable
// design: there is no rate to guess at and nothing to keep in step.
type livekitPublisher struct {
	media *media.Manager

	mu   sync.Mutex
	bots map[string]*publishing
}

// publishing is one bot's live connection.
type publishing struct {
	room   *lksdk.Room
	roomID string
	track  *lksdk.LocalTrack
	pub    *lksdk.LocalTrackPublication
	// generation tells a completion callback whether it is still the current
	// track: skipping fires the old track's callback, and acting on it would
	// advance the queue twice.
	generation int
}

// NewPublisher builds the LiveKit-backed publisher.
func NewPublisher(m *media.Manager) Publisher {
	return &livekitPublisher{media: m, bots: make(map[string]*publishing)}
}

func (p *livekitPublisher) Enabled() bool { return p.media != nil && p.media.Enabled() }

func (p *livekitPublisher) Join(botID, roomID, name string) error {
	p.mu.Lock()
	live, ok := p.bots[botID]
	p.mu.Unlock()

	// Already in the right room: nothing to do. Reconnecting would drop the
	// music for everyone listening.
	if ok && live.roomID == roomID && live.room.ConnectionState() == lksdk.ConnectionStateConnected {
		return nil
	}

	p.Leave(botID)

	credentials, err := p.media.IssueBotToken(roomID, BotIdentity(botID), name)
	if err != nil {
		return err
	}

	room, err := lksdk.ConnectToRoomWithToken(credentials.URL, credentials.Token, &lksdk.RoomCallback{})
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.bots[botID] = &publishing{room: room, roomID: roomID}
	p.mu.Unlock()

	slog.Info("bot joined the media session", "bot", botID, "room", roomID)
	return nil
}

func (p *livekitPublisher) Play(botID, path string, onDone func()) error {
	p.mu.Lock()
	live, ok := p.bots[botID]
	if !ok {
		p.mu.Unlock()
		return ErrNoRoom
	}

	// Whatever was playing stops first: two tracks from one participant would
	// simply be heard on top of each other.
	previous := live.pub
	live.pub = nil
	live.track = nil
	live.generation++
	generation := live.generation
	room := live.room
	p.mu.Unlock()

	if previous != nil {
		_ = room.LocalParticipant.UnpublishTrack(previous.SID())
	}

	track, err := lksdk.NewLocalFileTrack(path, lksdk.ReaderTrackWithOnWriteComplete(func() {
		// Only the track that is still current may advance the queue.
		p.mu.Lock()
		current := p.bots[botID]
		stale := current == nil || current.generation != generation
		p.mu.Unlock()

		if !stale && onDone != nil {
			onDone()
		}
	}))
	if err != nil {
		return err
	}

	pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "music",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		return err
	}

	p.mu.Lock()
	if live, ok := p.bots[botID]; ok && live.generation == generation {
		live.track = track
		live.pub = pub
	}
	p.mu.Unlock()

	return nil
}

func (p *livekitPublisher) Stop(botID string) {
	p.mu.Lock()
	live, ok := p.bots[botID]
	if !ok {
		p.mu.Unlock()
		return
	}

	pub := live.pub
	live.pub = nil
	live.track = nil
	live.generation++
	room := live.room
	p.mu.Unlock()

	if pub != nil {
		_ = room.LocalParticipant.UnpublishTrack(pub.SID())
	}
}

func (p *livekitPublisher) Leave(botID string) {
	p.mu.Lock()
	live, ok := p.bots[botID]
	delete(p.bots, botID)
	p.mu.Unlock()

	if !ok {
		return
	}

	live.generation++
	live.room.Disconnect()
	slog.Info("bot left the media session", "bot", botID, "room", live.roomID)
}

// BotIdentity is how a bot appears in LiveKit. The prefix keeps it from ever
// colliding with a client UUID, and the client keys its room tile on the same
// string.
func BotIdentity(botID string) string { return "bot-" + botID }
