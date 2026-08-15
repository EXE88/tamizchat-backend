package bots

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
)

// Errors the gateway maps onto protocol error codes.
var (
	ErrNotFound   = errors.New("bot not found")
	ErrDisabled   = errors.New("bot is disabled")
	ErrEmptyQueue = errors.New("bot has nothing to play")
	ErrBadAction  = errors.New("unknown bot action")
	ErrNoRoom     = errors.New("bot is not in a room")
	ErrNoMedia    = errors.New("media is not configured")
)

// ValidationError carries a message meant to be shown to the user as-is.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// trackTokenTTL bounds how long the URL handed to Ingress stays valid. It only
// needs to survive the fetch of one track.
const trackTokenTTL = 6 * time.Hour

// Ingress is the slice of the media manager the bots need. Keeping it an
// interface is what lets the tests run the whole bot engine without LiveKit.
type Ingress interface {
	Enabled() bool
	CreateIngress(ctx context.Context, roomID, identity, name, url string) (string, error)
	DeleteIngress(ctx context.Context, ingressID string) error
}

// Broadcaster reaches every connected user; session.Manager satisfies it.
type Broadcaster interface {
	Broadcast(typ string, payload any, exceptUUID string)
}

// Rooms is the room lookup the bots need.
type Rooms interface {
	Exists(roomID string) bool
}

// bot is one bot's configuration plus what it is doing right now.
type bot struct {
	def    storage.Bot
	tracks []Track

	roomID    string
	state     string
	index     int
	ingressID string
	// startedAt is when the current track began, and shortRuns counts how many
	// tracks in a row have ended almost immediately. A broken audio path — a
	// file LiveKit cannot decode, an Ingress that cannot reach the media port —
	// otherwise turns "advance at the end of a track" into a loop that walks the
	// whole queue several times a second, forever.
	startedAt time.Time
	shortRuns int
}

// shortRun is how long a track has to last to count as having really played,
// and howManyShortRuns is how many failures in a row stop the bot.
const (
	shortRun         = 3 * time.Second
	howManyShortRuns = 3
)

// trackGrant is permission to fetch one file, once minted, until it expires.
type trackGrant struct {
	botID   string
	path    string
	title   string
	expires time.Time
}

// Manager owns every bot on the server.
type Manager struct {
	cfg     *config.Config
	store   *storage.Store
	ingress Ingress
	rooms   Rooms
	global  Broadcaster

	mu   sync.Mutex
	bots map[string]*bot
	// playlists is every bot's playlists, by playlist id.
	playlists map[string]storage.Playlist
	// grants are outstanding permissions for Ingress to fetch one track.
	//
	// One per *track*, not one per bot. A bot used to hold a single token that
	// the next play replaced, so an Ingress still fetching the previous track
	// got a 404, reported the track as ended, and the queue advanced — which
	// started the next track, replaced the token again, and went round forever.
	// A grant also remembers which file it was minted for, so a fetch can never
	// be answered with whatever the queue happens to point at now.
	grants map[string]trackGrant
	// tickets are outstanding permissions to upload one track.
	tickets map[string]*trackTicket
}

// New loads the configured bots. Bots start idle: what a bot was playing before
// a restart is runtime state, and nobody is in the room to hear it anyway.
func New(ctx context.Context, cfg *config.Config, store *storage.Store,
	ingress Ingress, rooms Rooms, global Broadcaster) (*Manager, error) {
	defs, err := store.ListBots(ctx)
	if err != nil {
		return nil, err
	}

	m := &Manager{
		cfg:       cfg,
		store:     store,
		ingress:   ingress,
		rooms:     rooms,
		global:    global,
		bots:      make(map[string]*bot, len(defs)),
		playlists: make(map[string]storage.Playlist),
		grants:    make(map[string]trackGrant),
		tickets:   make(map[string]*trackTicket),
	}

	lists, err := store.ListPlaylists(ctx)
	if err != nil {
		return nil, err
	}
	for _, def := range lists {
		m.playlists[def.ID] = def
	}

	for _, def := range defs {
		b := &bot{def: def, state: protocol.BotIdle}
		m.bots[def.ID] = b
		m.rescanLocked(b)
		if len(b.tracks) == 0 && def.Enabled {
			slog.Warn("bot has nothing to play",
				"bot", def.Name, "folder", def.Folder, "playlist", def.PlaylistID)
		}
	}

	slog.Info("bots loaded", "count", len(defs))
	return m, nil
}

// Views renders every bot for a client.
func (m *Manager) Views() []protocol.Bot {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]protocol.Bot, 0, len(m.bots))
	for _, b := range m.bots {
		out = append(out, m.viewLocked(b))
	}
	sortBots(out)
	return out
}

// view builds the public form. The caller must hold m.mu.
func (b *bot) view() protocol.Bot {
	v := protocol.Bot{
		ID:         b.def.ID,
		Name:       b.def.Name,
		Kind:       b.def.Kind,
		Color:      b.def.Color,
		RoomID:     b.roomID,
		State:      b.state,
		TrackCount: len(b.tracks),
		Loop:       b.def.LoopQueue,
		Shuffle:    b.def.Shuffle,
		Enabled:    b.def.Enabled,
	}
	if b.index >= 0 && b.index < len(b.tracks) {
		v.Track = &protocol.BotTrack{Index: b.index, Title: b.tracks[b.index].Title}
	}
	return v
}

// viewLocked builds the public form, including which playlist the bot is
// playing from. The caller must hold m.mu.
func (m *Manager) viewLocked(b *bot) protocol.Bot {
	v := b.view()
	if id := b.def.PlaylistID; id != "" {
		v.PlaylistID = id
		if def, ok := m.playlists[id]; ok {
			v.PlaylistName = def.Name
		}
	}
	return v
}

// Queue returns a bot's full track list.
func (m *Manager) Queue(botID string) (protocol.BotQueue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.bots[botID]
	if !ok {
		return protocol.BotQueue{}, ErrNotFound
	}

	tracks := make([]protocol.BotTrack, 0, len(b.tracks))
	for i, t := range b.tracks {
		tracks = append(tracks, protocol.BotTrack{Index: i, Title: t.Title})
	}
	return protocol.BotQueue{BotID: botID, Tracks: tracks}, nil
}

// Move sends a bot to a room, or out of every room when roomID is empty.
// Playback stops on the way out: audio belongs to the room it was started in.
func (m *Manager) Move(ctx context.Context, actorUUID, botID, roomID string) (protocol.Bot, error) {
	if roomID != "" && !m.rooms.Exists(roomID) {
		return protocol.Bot{}, ErrNoRoom
	}

	m.mu.Lock()
	b, ok := m.bots[botID]
	if !ok {
		m.mu.Unlock()
		return protocol.Bot{}, ErrNotFound
	}
	if !b.def.Enabled {
		m.mu.Unlock()
		return protocol.Bot{}, ErrDisabled
	}
	previous := b.ingressID
	b.ingressID = ""
	b.roomID = roomID
	b.state = protocol.BotIdle
	if roomID != "" {
		b.state = protocol.BotStopped
	}
	view := m.viewLocked(b)
	m.mu.Unlock()

	m.stopIngress(ctx, previous)
	m.announce(actorUUID, view)
	slog.Info("bot moved", "bot", view.Name, "room", roomID)
	return view, nil
}

// Control drives playback.
func (m *Manager) Control(ctx context.Context, actorUUID string, req protocol.BotControl) (protocol.Bot, error) {
	switch req.Action {
	case protocol.BotActionStop:
		return m.stop(ctx, actorUUID, req.BotID)
	case protocol.BotActionPlay:
		return m.play(ctx, actorUUID, req.BotID, 0, false)
	case protocol.BotActionNext:
		return m.play(ctx, actorUUID, req.BotID, 1, true)
	case protocol.BotActionPrev:
		return m.play(ctx, actorUUID, req.BotID, -1, true)
	case protocol.BotActionSelect:
		return m.selectTrack(ctx, actorUUID, req.BotID, req.TrackIndex)
	default:
		return protocol.Bot{}, ErrBadAction
	}
}

// play starts the current track, optionally stepping the queue first. step is
// applied only when move is true, so "play" resumes the selected track while
// "next" advances.
func (m *Manager) play(ctx context.Context, actorUUID, botID string, step int, move bool) (protocol.Bot, error) {
	if !m.ingress.Enabled() {
		return protocol.Bot{}, ErrNoMedia
	}

	m.mu.Lock()
	b, ok := m.bots[botID]
	if !ok {
		m.mu.Unlock()
		return protocol.Bot{}, ErrNotFound
	}
	if !b.def.Enabled {
		m.mu.Unlock()
		return protocol.Bot{}, ErrDisabled
	}
	if b.roomID == "" {
		m.mu.Unlock()
		return protocol.Bot{}, ErrNoRoom
	}
	if len(b.tracks) == 0 {
		m.mu.Unlock()
		return protocol.Bot{}, ErrEmptyQueue
	}

	if move {
		b.advance(step)
	}

	base, err := m.publicBase()
	if err != nil {
		m.mu.Unlock()
		return protocol.Bot{}, err
	}

	previous := b.ingressID
	b.ingressID = ""
	token := m.grantTrackLocked(b)

	roomID, identity, name := b.roomID, b.identity(), b.def.Name
	streamURL := fmt.Sprintf("%s/api/v1/bot-stream/%s?token=%s",
		base, url.PathEscape(b.def.ID), url.QueryEscape(token))
	m.mu.Unlock()

	// The old ingress goes first: two ingresses publishing as the same identity
	// would fight over the participant.
	m.stopIngress(ctx, previous)

	ingressID, err := m.ingress.CreateIngress(ctx, roomID, identity, name, streamURL)
	if err != nil {
		return protocol.Bot{}, err
	}

	m.mu.Lock()
	b.ingressID = ingressID
	b.state = protocol.BotPlaying
	b.startedAt = time.Now()
	view := m.viewLocked(b)
	m.mu.Unlock()

	m.announce(actorUUID, view)
	slog.Info("bot playing", "bot", name, "room", roomID, "track", view.Track)
	return view, nil
}

func (m *Manager) selectTrack(ctx context.Context, actorUUID, botID string, index int) (protocol.Bot, error) {
	m.mu.Lock()
	b, ok := m.bots[botID]
	if !ok {
		m.mu.Unlock()
		return protocol.Bot{}, ErrNotFound
	}
	if index < 0 || index >= len(b.tracks) {
		m.mu.Unlock()
		return protocol.Bot{}, &ValidationError{Msg: "that track number is not in the queue"}
	}
	b.index = index
	m.mu.Unlock()

	return m.play(ctx, actorUUID, botID, 0, false)
}

// stop ends playback and leaves the bot in the room, silent.
func (m *Manager) stop(ctx context.Context, actorUUID, botID string) (protocol.Bot, error) {
	m.mu.Lock()
	b, ok := m.bots[botID]
	if !ok {
		m.mu.Unlock()
		return protocol.Bot{}, ErrNotFound
	}
	previous := b.ingressID
	b.ingressID = ""
	if b.roomID != "" {
		b.state = protocol.BotStopped
	} else {
		b.state = protocol.BotIdle
	}
	view := m.viewLocked(b)
	m.mu.Unlock()

	m.stopIngress(ctx, previous)
	m.announce(actorUUID, view)
	return view, nil
}

// TrackEnded is called when LiveKit reports that an ingress finished, which is
// how a bot learns its track ran out. The queue then advances on its own — a
// music bot that stops after one song would be useless.
func (m *Manager) TrackEnded(ctx context.Context, ingressID string) {
	m.mu.Lock()
	var target *bot
	for _, b := range m.bots {
		if b.ingressID == ingressID {
			target = b
			break
		}
	}
	if target == nil {
		m.mu.Unlock()
		return // an ingress we already replaced, or somebody else's
	}

	botID := target.def.ID
	target.ingressID = ""

	// A track that ended almost as soon as it started did not play: LiveKit
	// could not decode it, or could not reach the media port. Advancing is the
	// right answer once — the next file may be fine — but doing it forever is
	// how one broken bot ends up making hundreds of ingresses a minute.
	if time.Since(target.startedAt) < shortRun {
		target.shortRuns++
	} else {
		target.shortRuns = 0
	}

	if target.shortRuns >= howManyShortRuns {
		target.shortRuns = 0
		m.mu.Unlock()

		slog.Warn("bot stopped: its tracks keep ending immediately — check that "+
			"the audio really plays and that Ingress can reach this server",
			"bot", botID)

		if _, err := m.stop(ctx, "", botID); err != nil {
			slog.Debug("stopping a bot that cannot play", "bot", botID, "err", err)
		}
		return
	}

	atEnd := !target.def.LoopQueue && !target.def.Shuffle && target.index >= len(target.tracks)-1
	m.mu.Unlock()

	if atEnd {
		if _, err := m.stop(ctx, "", botID); err != nil {
			slog.Debug("stopping bot at end of queue", "bot", botID, "err", err)
		}
		return
	}
	if _, err := m.play(ctx, "", botID, 1, true); err != nil {
		slog.Warn("bot could not continue to the next track", "bot", botID, "err", err)
	}
}

// ResolveTrack turns an Ingress fetch into a file to serve.
//
// The grant names the file, so a fetch is always answered with the track it was
// issued for — never with whatever the queue moved on to in the meantime, which
// is what "the wrong song played" would have looked like.
func (m *Manager) ResolveTrack(botID, token string) (path, filename string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if token == "" {
		return "", "", ErrNotFound
	}

	grant, ok := m.grants[token]
	if !ok || grant.botID != botID || time.Now().After(grant.expires) {
		return "", "", ErrNotFound
	}
	if grant.path == "" {
		return "", "", ErrEmptyQueue
	}

	b, ok := m.bots[botID]
	if !ok {
		return "", "", ErrNotFound
	}

	// Re-checked at fetch time as well as at mint time: the file must still sit
	// in something this bot plays from. A playlist deleted between the two is
	// exactly the case that would otherwise serve a file out of a folder the bot
	// no longer has anything to do with.
	if !withinRoot(m.sourceDirLocked(b), grant.path) && !withinRoot(b.def.Folder, grant.path) {
		return "", "", ErrNotFound
	}
	return grant.path, grant.title, nil
}

// RoomGone stops any bot that was sitting in a room that no longer exists.
func (m *Manager) RoomGone(roomID string) {
	m.mu.Lock()
	var affected []string
	for id, b := range m.bots {
		if b.roomID == roomID {
			affected = append(affected, id)
		}
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, id := range affected {
		if _, err := m.Move(ctx, "", id, ""); err != nil {
			slog.Debug("moving bot out of a deleted room", "bot", id, "err", err)
		}
	}
}

// Reload re-reads the bot definitions and rescans their folders, so the panel's
// changes can be picked up without losing what is currently playing.
func (m *Manager) Reload(ctx context.Context) error {
	defs, err := m.store.ListBots(ctx)
	if err != nil {
		return err
	}
	lists, err := m.store.ListPlaylists(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.playlists = make(map[string]storage.Playlist, len(lists))
	for _, def := range lists {
		m.playlists[def.ID] = def
	}

	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		seen[def.ID] = true
		b, ok := m.bots[def.ID]
		if !ok {
			b = &bot{state: protocol.BotIdle}
			m.bots[def.ID] = b
		}
		b.def = def
		m.rescanLocked(b)
	}
	for id := range m.bots {
		if !seen[id] {
			delete(m.bots, id)
		}
	}
	return nil
}

// advance steps the queue, honouring shuffle and loop.
func (b *bot) advance(step int) {
	if len(b.tracks) == 0 {
		return
	}
	if b.def.Shuffle && len(b.tracks) > 1 {
		for {
			next := rand.IntN(len(b.tracks))
			if next != b.index {
				b.index = next
				return
			}
		}
	}

	next := b.index + step
	switch {
	case next < 0:
		if b.def.LoopQueue {
			next = len(b.tracks) - 1
		} else {
			next = 0
		}
	case next >= len(b.tracks):
		if b.def.LoopQueue {
			next = 0
		} else {
			next = len(b.tracks) - 1
		}
	}
	b.index = next
}

// identity is the bot's participant identity in LiveKit. The prefix keeps it
// from ever colliding with a real client UUID.
func (b *bot) identity() string { return "bot-" + b.def.ID }

// grantTrackLocked mints permission to fetch the track the bot is on right now.
// The caller must hold m.mu.
func (m *Manager) grantTrackLocked(b *bot) string {
	m.sweepGrantsLocked()

	var raw [24]byte
	if _, err := crand.Read(raw[:]); err != nil {
		panic("tamizchat: crypto/rand failed: " + err.Error())
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])

	grant := trackGrant{botID: b.def.ID, expires: time.Now().Add(trackTokenTTL)}
	if b.index >= 0 && b.index < len(b.tracks) {
		grant.path = b.tracks[b.index].Path
		grant.title = b.tracks[b.index].Title
	}

	m.grants[token] = grant
	return token
}

// dropGrantsLocked forgets every grant belonging to a bot. Used when the bot is
// deleted; stopping deliberately does not, because a fetch already under way is
// what makes the last few seconds of a track arrive.
func (m *Manager) dropGrantsLocked(botID string) {
	for token, grant := range m.grants {
		if grant.botID == botID {
			delete(m.grants, token)
		}
	}
}

func (m *Manager) sweepGrantsLocked() {
	now := time.Now()
	for token, grant := range m.grants {
		if now.After(grant.expires) {
			delete(m.grants, token)
		}
	}
}

// stopIngress ends an ingress without letting a media failure block the caller.
func (m *Manager) stopIngress(ctx context.Context, ingressID string) {
	if ingressID == "" {
		return
	}
	if err := m.ingress.DeleteIngress(ctx, ingressID); err != nil {
		slog.Debug("stopping ingress", "ingress", ingressID, "err", err)
	}
}

// announce tells every connected client what a bot is doing now.
func (m *Manager) announce(actorUUID string, view protocol.Bot) {
	if m.global != nil {
		m.global.Broadcast(protocol.TypeBotState, view, actorUUID)
	}
}

// publicBase is the address Ingress will fetch from. It must be reachable from
// the LiveKit host, which the server cannot work out on its own — a bare
// listen address like ":8080" says nothing about how anyone else reaches it.
func (m *Manager) publicBase() (string, error) {
	host := strings.TrimSpace(m.cfg.String(config.KeyPublicHost))
	if host == "" {
		return "", &ValidationError{
			Msg: "to play music you must set \"Public host\" in the network settings, " +
				"so LiveKit can fetch the file from this server",
		}
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	return strings.TrimRight(host, "/"), nil
}

func sortBots(list []protocol.Bot) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && strings.ToLower(list[j-1].Name) > strings.ToLower(list[j].Name); j-- {
			list[j-1], list[j] = list[j], list[j-1]
		}
	}
}
