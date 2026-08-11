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
	// token authorizes Ingress to fetch the current track.
	token   string
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
	// byToken resolves an Ingress fetch back to the bot it belongs to.
	byToken map[string]string
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
		cfg:     cfg,
		store:   store,
		ingress: ingress,
		rooms:   rooms,
		global:  global,
		bots:    make(map[string]*bot, len(defs)),
		byToken: make(map[string]string),
	}

	for _, def := range defs {
		b := &bot{def: def, state: protocol.BotIdle}
		if tracks, err := scanFolder(def.Folder); err == nil {
			b.tracks = tracks
		} else if def.Enabled {
			slog.Warn("bot folder could not be scanned",
				"bot", def.Name, "folder", def.Folder, "err", err)
		}
		m.bots[def.ID] = b
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
		out = append(out, b.view())
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
	view := b.view()
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
	m.issueTokenLocked(b)

	roomID, identity, name := b.roomID, b.identity(), b.def.Name
	streamURL := fmt.Sprintf("%s/api/v1/bot-stream/%s?token=%s",
		base, url.PathEscape(b.def.ID), url.QueryEscape(b.token))
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
	view := b.view()
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
		return protocol.Bot{}, &ValidationError{Msg: "شمارهٔ آهنگ در فهرست نیست"}
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
	m.dropTokenLocked(b)
	if b.roomID != "" {
		b.state = protocol.BotStopped
	} else {
		b.state = protocol.BotIdle
	}
	view := b.view()
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

// ResolveTrack turns an Ingress fetch into a file to serve. It is deliberately
// strict: the token must be the bot's current one, and the file must still sit
// inside the configured folder.
func (m *Manager) ResolveTrack(botID, token string) (path, filename string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.bots[botID]
	if !ok {
		return "", "", ErrNotFound
	}
	if b.token == "" || token == "" || b.token != token {
		return "", "", ErrNotFound
	}
	if time.Now().After(b.expires) {
		return "", "", ErrNotFound
	}
	if b.index < 0 || b.index >= len(b.tracks) {
		return "", "", ErrEmptyQueue
	}

	track := b.tracks[b.index]
	if !withinRoot(b.def.Folder, track.Path) {
		return "", "", ErrNotFound
	}
	return track.Path, track.Title, nil
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

	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		seen[def.ID] = true
		b, ok := m.bots[def.ID]
		if !ok {
			b = &bot{state: protocol.BotIdle}
			m.bots[def.ID] = b
		}
		b.def = def
		if tracks, err := scanFolder(def.Folder); err == nil {
			b.tracks = tracks
			if b.index >= len(tracks) {
				b.index = 0
			}
		}
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

// issueTokenLocked mints a fresh fetch token. The caller must hold m.mu.
func (m *Manager) issueTokenLocked(b *bot) {
	m.dropTokenLocked(b)

	var raw [24]byte
	if _, err := crand.Read(raw[:]); err != nil {
		panic("tamizchat: crypto/rand failed: " + err.Error())
	}
	b.token = base64.RawURLEncoding.EncodeToString(raw[:])
	b.expires = time.Now().Add(trackTokenTTL)
	m.byToken[b.token] = b.def.ID
}

func (m *Manager) dropTokenLocked(b *bot) {
	if b.token != "" {
		delete(m.byToken, b.token)
		b.token = ""
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
			Msg: "برای پخش موسیقی باید «هاست عمومی» را در تنظیمات شبکه مقدار بدهید، " +
				"تا LiveKit بتواند فایل را از این سرور بگیرد",
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
