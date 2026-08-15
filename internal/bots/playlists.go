package bots

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
	"tamizchat/internal/textutil"
)

// maxPlaylists bounds how many playlists one bot may have. A playlist is a
// folder; this is about keeping a bot's configuration comprehensible, not about
// disk space, which the quota covers.
const maxPlaylists = 32

// trackTicketTTL is how long a client has to actually send the bytes after
// asking permission to upload a track. It matches the room-file flow.
const trackTicketTTL = 2 * time.Minute

// Errors the gateway maps onto protocol error codes.
var (
	ErrPlaylistNotFound = errors.New("playlist not found")
	ErrPlaylistTaken    = errors.New("playlist name already used")
	ErrTooManyPlaylists = errors.New("too many playlists")
	ErrNotAudio         = errors.New("that file is not audio")
	ErrTrackNotFound    = errors.New("track not found")
	ErrTooLarge         = errors.New("file too large")
	ErrQuotaFull        = errors.New("the bot's music storage is full")
	ErrBadTicket        = errors.New("the upload ticket is not valid")
	ErrEmptyUpload      = errors.New("the upload was empty")
)

// trackTicket is permission to upload exactly one track, once.
type trackTicket struct {
	token string
	botID string
	// clientUUID is who asked, so the upload can be announced to everyone but
	// them — they get the bot's new state in the HTTP reply.
	clientUUID string
	playlistID string
	name       string
	maxSize    int64
	expiresAt  time.Time
}

// Playlists lists a bot's playlists with what is in each of them.
func (m *Manager) Playlists(botID string) (protocol.BotPlaylistList, error) {
	m.mu.Lock()
	b, ok := m.bots[botID]
	if !ok {
		m.mu.Unlock()
		return protocol.BotPlaylistList{}, ErrNotFound
	}
	active := b.def.PlaylistID
	defs := m.playlistsOfLocked(botID)
	m.mu.Unlock()

	out := protocol.BotPlaylistList{
		BotID:     botID,
		Active:    active,
		Playlists: make([]protocol.BotPlaylist, 0, len(defs)),
	}
	for _, def := range defs {
		// Counting means reading the folder, which is why it happens outside the
		// lock: everything else in this manager waits on that mutex, including
		// the frames that carry audio decisions.
		count, _ := CountPlayable(m.playlistDir(botID, def.ID))
		out.Playlists = append(out.Playlists, protocol.BotPlaylist{
			ID: def.ID, BotID: botID, Name: def.Name, TrackCount: count,
		})
	}
	return out, nil
}

// CreatePlaylist adds an empty playlist and the folder behind it.
func (m *Manager) CreatePlaylist(ctx context.Context, spec protocol.BotPlaylistSpec) (protocol.BotPlaylist, error) {
	name, err := textutil.NormalizeName(spec.Name, "playlist name", 1, 48)
	if err != nil {
		return protocol.BotPlaylist{}, &ValidationError{Msg: err.Error()}
	}

	m.mu.Lock()
	if _, ok := m.bots[spec.BotID]; !ok {
		m.mu.Unlock()
		return protocol.BotPlaylist{}, ErrNotFound
	}
	if len(m.playlistsOfLocked(spec.BotID)) >= maxPlaylists {
		m.mu.Unlock()
		return protocol.BotPlaylist{}, ErrTooManyPlaylists
	}
	m.mu.Unlock()

	def := storage.Playlist{ID: storage.NewUUID(), BotID: spec.BotID, Name: name}
	if err := os.MkdirAll(m.playlistDir(def.BotID, def.ID), 0o750); err != nil {
		return protocol.BotPlaylist{}, fmt.Errorf("create playlist folder: %w", err)
	}
	if err := m.store.CreatePlaylist(ctx, def); err != nil {
		if errors.Is(err, storage.ErrPlaylistNameTaken) {
			return protocol.BotPlaylist{}, ErrPlaylistTaken
		}
		return protocol.BotPlaylist{}, err
	}

	m.mu.Lock()
	m.playlists[def.ID] = def
	m.mu.Unlock()

	return protocol.BotPlaylist{ID: def.ID, BotID: def.BotID, Name: def.Name}, nil
}

// RenamePlaylist is the only edit a playlist has: its tracks are files, and
// they are managed by uploading and deleting them.
func (m *Manager) RenamePlaylist(ctx context.Context, spec protocol.BotPlaylistSpec) (protocol.BotPlaylist, error) {
	name, err := textutil.NormalizeName(spec.Name, "playlist name", 1, 48)
	if err != nil {
		return protocol.BotPlaylist{}, &ValidationError{Msg: err.Error()}
	}

	def, err := m.playlistOf(spec.BotID, spec.PlaylistID)
	if err != nil {
		return protocol.BotPlaylist{}, err
	}
	if err := m.store.RenamePlaylist(ctx, def.ID, name); err != nil {
		switch {
		case errors.Is(err, storage.ErrPlaylistNameTaken):
			return protocol.BotPlaylist{}, ErrPlaylistTaken
		case errors.Is(err, storage.ErrPlaylistNotFound):
			return protocol.BotPlaylist{}, ErrPlaylistNotFound
		}
		return protocol.BotPlaylist{}, err
	}

	def.Name = name
	count, _ := CountPlayable(m.playlistDir(def.BotID, def.ID))

	m.mu.Lock()
	m.playlists[def.ID] = def
	b, ok := m.bots[def.BotID]
	active := ok && b.def.PlaylistID == def.ID
	var view protocol.Bot
	if active {
		view = m.viewLocked(b)
	}
	m.mu.Unlock()

	// The playlist's name is part of what a client draws for a playing bot, so
	// renaming the active one is a bot state change too.
	if active {
		m.announce("", view)
	}
	return protocol.BotPlaylist{ID: def.ID, BotID: def.BotID, Name: def.Name, TrackCount: count}, nil
}

// DeletePlaylist removes a playlist and the music in it. If the bot was playing
// from it, it falls back to its own folder and stops.
func (m *Manager) DeletePlaylist(ctx context.Context, actorUUID string, spec protocol.BotPlaylistSpec) error {
	def, err := m.playlistOf(spec.BotID, spec.PlaylistID)
	if err != nil {
		return err
	}

	m.mu.Lock()
	b, ok := m.bots[def.BotID]
	wasActive := ok && b.def.PlaylistID == def.ID
	m.mu.Unlock()

	if wasActive {
		// Selecting nothing stops playback first, so the folder is not being
		// read by Ingress while it is deleted.
		if _, err := m.SelectPlaylist(ctx, actorUUID, protocol.BotPlaylistSpec{BotID: def.BotID}); err != nil {
			return err
		}
	}

	if err := m.store.DeletePlaylist(ctx, def.ID); err != nil {
		if errors.Is(err, storage.ErrPlaylistNotFound) {
			return ErrPlaylistNotFound
		}
		return err
	}

	m.mu.Lock()
	delete(m.playlists, def.ID)
	m.mu.Unlock()

	if err := os.RemoveAll(m.playlistDir(def.BotID, def.ID)); err != nil {
		slog.Warn("could not remove a deleted playlist's folder",
			"playlist", def.Name, "err", err)
	}
	slog.Info("playlist deleted", "playlist", def.Name, "bot", def.BotID)
	return nil
}

// SelectPlaylist decides what the bot plays from. An empty playlist id means
// its own folder, which is what a bot configured from the CLI panel uses.
//
// Playback stops on the way: the queue is about to be a different list, and
// carrying an index across it would resume at an unrelated track.
func (m *Manager) SelectPlaylist(ctx context.Context, actorUUID string, spec protocol.BotPlaylistSpec) (protocol.Bot, error) {
	if spec.PlaylistID != "" {
		if _, err := m.playlistOf(spec.BotID, spec.PlaylistID); err != nil {
			return protocol.Bot{}, err
		}
	}

	m.mu.Lock()
	b, ok := m.bots[spec.BotID]
	if !ok {
		m.mu.Unlock()
		return protocol.Bot{}, ErrNotFound
	}
	def := b.def
	m.mu.Unlock()

	def.PlaylistID = spec.PlaylistID
	if err := m.store.UpdateBot(ctx, def); err != nil {
		if errors.Is(err, storage.ErrBotNotFound) {
			return protocol.Bot{}, ErrNotFound
		}
		return protocol.Bot{}, err
	}

	m.mu.Lock()
	b.def = def
	b.index = 0
	m.rescanLocked(b)
	m.mu.Unlock()

	return m.stop(ctx, actorUUID, spec.BotID)
}

// RequestTrackUpload checks everything it can before a byte is sent — the bot,
// the playlist, the file type and the quota — and hands back a single-use
// ticket. This is the same shape as the room-file upload: permission travels
// over the socket, the bytes go over HTTP.
func (m *Manager) RequestTrackUpload(clientUUID string, req protocol.BotTrackUploadRequest) (protocol.BotTrackUploadTicket, error) {
	def, err := m.playlistOf(req.BotID, req.PlaylistID)
	if err != nil {
		return protocol.BotTrackUploadTicket{}, err
	}

	name, err := trackFileName(req.Name)
	if err != nil {
		return protocol.BotTrackUploadTicket{}, err
	}

	maxSize := m.maxTrackSize()
	if req.Size > maxSize {
		return protocol.BotTrackUploadTicket{}, ErrTooLarge
	}
	// A declared size of zero means "I do not know yet"; the ceiling still
	// applies while the bytes arrive.
	if req.Size > 0 && m.botUsage(def.BotID)+req.Size > m.quota() {
		return protocol.BotTrackUploadTicket{}, ErrQuotaFull
	}

	t := &trackTicket{
		token:      newToken(),
		botID:      def.BotID,
		clientUUID: clientUUID,
		playlistID: def.ID,
		name:       name,
		maxSize:    maxSize,
		expiresAt:  time.Now().Add(trackTicketTTL),
	}

	m.mu.Lock()
	m.sweepTicketsLocked()
	m.tickets[t.token] = t
	m.mu.Unlock()

	return protocol.BotTrackUploadTicket{
		URL:       "/api/v1/bot-track?token=" + t.token,
		Token:     t.token,
		ExpiresAt: t.expiresAt.Unix(),
		MaxSize:   maxSize,
	}, nil
}

// UploadTrack consumes a ticket and writes the bytes into the playlist folder.
// It returns the bot's new state, so the caller can tell everyone the queue
// grew.
func (m *Manager) UploadTrack(ctx context.Context, token string, body io.Reader) (protocol.Bot, error) {
	m.mu.Lock()
	m.sweepTicketsLocked()
	t, ok := m.tickets[token]
	if ok {
		// One ticket, one upload, successful or not.
		delete(m.tickets, token)
	}
	m.mu.Unlock()

	if !ok || time.Now().After(t.expiresAt) {
		return protocol.Bot{}, ErrBadTicket
	}
	if _, err := m.playlistOf(t.botID, t.playlistID); err != nil {
		return protocol.Bot{}, err
	}

	dir := m.playlistDir(t.botID, t.playlistID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return protocol.Bot{}, fmt.Errorf("create playlist folder: %w", err)
	}

	path, err := freeName(dir, t.name)
	if err != nil {
		return protocol.Bot{}, err
	}
	// The name was cleaned of separators, but the check that it really landed
	// inside the folder is cheap and is the one that matters.
	if !withinRoot(dir, path) {
		return protocol.Bot{}, ErrNotAudio
	}

	size, err := writeLimited(path, body, t.maxSize)
	if err != nil {
		removeQuietly(path)
		return protocol.Bot{}, err
	}
	if m.botUsage(t.botID) > m.quota() {
		removeQuietly(path)
		return protocol.Bot{}, ErrQuotaFull
	}

	slog.Info("bot track stored", "bot", t.botID, "playlist", t.playlistID,
		"name", filepath.Base(path), "size", size)
	return m.refreshQueue(t.clientUUID, t.botID)
}

// DeleteTrack removes one track from a playlist by its position in that
// playlist's queue.
func (m *Manager) DeleteTrack(ctx context.Context, actorUUID string, ref protocol.BotTrackRef) (protocol.Bot, error) {
	def, err := m.playlistOf(ref.BotID, ref.PlaylistID)
	if err != nil {
		return protocol.Bot{}, err
	}

	dir := m.playlistDir(def.BotID, def.ID)
	tracks, err := scanFolder(dir)
	if err != nil {
		return protocol.Bot{}, ErrTrackNotFound
	}
	if ref.Index < 0 || ref.Index >= len(tracks) {
		return protocol.Bot{}, ErrTrackNotFound
	}

	track := tracks[ref.Index]
	if !withinRoot(dir, track.Path) {
		return protocol.Bot{}, ErrTrackNotFound
	}
	if err := os.Remove(track.Path); err != nil {
		return protocol.Bot{}, fmt.Errorf("delete track: %w", err)
	}

	slog.Info("bot track deleted", "bot", def.BotID, "playlist", def.ID, "track", track.Title)
	return m.refreshQueue(actorUUID, def.BotID)
}

// PlaylistQueue lists the tracks of one playlist, which is what an admin
// screen shows while filling it. The bot's own queue is Queue.
func (m *Manager) PlaylistQueue(botID, playlistID string) (protocol.BotQueue, error) {
	def, err := m.playlistOf(botID, playlistID)
	if err != nil {
		return protocol.BotQueue{}, err
	}

	tracks, err := scanFolder(m.playlistDir(def.BotID, def.ID))
	if err != nil {
		// An empty folder that has not been written to yet is not an error.
		return protocol.BotQueue{BotID: botID}, nil
	}

	out := protocol.BotQueue{BotID: botID, Tracks: make([]protocol.BotTrack, 0, len(tracks))}
	for i, t := range tracks {
		out.Tracks = append(out.Tracks, protocol.BotTrack{Index: i, Title: t.Title})
	}
	return out, nil
}

// refreshQueue rescans whatever the bot plays from and announces the result to
// everyone but the actor, who has the same state in their own reply.
func (m *Manager) refreshQueue(actorUUID, botID string) (protocol.Bot, error) {
	m.mu.Lock()
	b, ok := m.bots[botID]
	if !ok {
		m.mu.Unlock()
		return protocol.Bot{}, ErrNotFound
	}
	m.rescanLocked(b)
	view := m.viewLocked(b)
	m.mu.Unlock()

	m.announce(actorUUID, view)
	return view, nil
}

// playlistOf loads a playlist and checks it really belongs to that bot, so an
// id from one bot cannot be used to write into another's folder.
func (m *Manager) playlistOf(botID, playlistID string) (storage.Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.bots[botID]; !ok {
		return storage.Playlist{}, ErrNotFound
	}
	def, ok := m.playlists[playlistID]
	if !ok || def.BotID != botID {
		return storage.Playlist{}, ErrPlaylistNotFound
	}
	return def, nil
}

// playlistsOfLocked returns one bot's playlists, oldest first. The caller must
// hold m.mu.
func (m *Manager) playlistsOfLocked(botID string) []storage.Playlist {
	var out []storage.Playlist
	for _, def := range m.playlists {
		if def.BotID == botID {
			out = append(out, def)
		}
	}
	sortPlaylists(out)
	return out
}

// playlistDir is where a playlist's files live: always under the server's own
// bot storage, never inside a folder an operator configured by hand.
func (m *Manager) playlistDir(botID, playlistID string) string {
	return filepath.Join(m.managedFolder(botID), playlistID)
}

// PlaylistFolder is the same path worked out from the bots.dir setting and the
// database location, for the CLI panel — which reads the database directly and
// has no manager. dbPath is what resolves a relative setting, exactly as
// storageRoot does.
func PlaylistFolder(root, dbPath, botID, playlistID string) string {
	if strings.TrimSpace(root) == "" {
		root = "bots"
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(dbPath), root)
	}
	return filepath.Join(root, botID, playlistID)
}

// sourceDirLocked is what the bot plays from right now. The caller must hold
// m.mu.
func (m *Manager) sourceDirLocked(b *bot) string {
	if b.def.PlaylistID != "" {
		return m.playlistDir(b.def.ID, b.def.PlaylistID)
	}
	return b.def.Folder
}

// rescanLocked refreshes a bot's queue from whatever it plays from. The caller
// must hold m.mu.
func (m *Manager) rescanLocked(b *bot) {
	tracks, err := scanFolder(m.sourceDirLocked(b))
	if err != nil {
		// A folder that cannot be read means an empty queue, not a stale one:
		// playing tracks that are no longer there would fail at Ingress with a
		// far less obvious message.
		b.tracks = nil
	} else {
		b.tracks = tracks
	}
	if b.index >= len(b.tracks) {
		b.index = 0
	}
}

// botUsage is how much disk one bot's playlists take up.
func (m *Manager) botUsage(botID string) int64 {
	var total int64
	root := m.managedFolder(botID)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (m *Manager) maxTrackSize() int64 {
	return int64(m.cfg.Int(config.KeyBotsMaxTrackMB)) << 20
}

func (m *Manager) quota() int64 {
	return int64(m.cfg.Int(config.KeyBotsQuotaMB)) << 20
}

// sweepTicketsLocked drops expired tickets. The caller must hold m.mu.
func (m *Manager) sweepTicketsLocked() {
	now := time.Now()
	for token, t := range m.tickets {
		if now.After(t.expiresAt) {
			delete(m.tickets, token)
		}
	}
}

// trackFileName cleans an uploaded track's name. Unlike a room file, this name
// really does become a path: the queue is the folder listing, and the title a
// listener sees is the file name. So it is stripped of anything that could
// escape the folder, and it must carry an extension the scanner accepts.
func trackFileName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)

	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7f: // control characters
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	name = strings.Trim(b.String(), " .")

	if name == "" || name == "." || name == ".." {
		return "", &ValidationError{Msg: "the track needs a file name"}
	}
	if runes := []rune(name); len(runes) > 120 {
		ext := filepath.Ext(name)
		name = string(runes[:120-len([]rune(ext))]) + ext
	}
	if !audioExtensions[strings.ToLower(filepath.Ext(name))] {
		return "", ErrNotAudio
	}
	return name, nil
}

// freeName finds a name that is not taken, so uploading two files called
// "intro.mp3" keeps both instead of one quietly replacing the other.
func freeName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	for i := 0; i < 100; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, i+1, ext)
		}
		path := filepath.Join(dir, candidate)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		}
	}
	return "", &ValidationError{Msg: "too many tracks with that name already"}
}

// writeLimited stores a body without trusting its length. It is deliberately
// the same shape as the room-file writer: read one byte past the ceiling, and
// if it arrives the upload is too big.
func writeLimited(path string, body io.Reader, max int64) (int64, error) {
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, fmt.Errorf("create track: %w", err)
	}
	defer dst.Close()

	written, err := io.Copy(dst, io.LimitReader(body, max+1))
	if err != nil {
		return 0, fmt.Errorf("write track: %w", err)
	}
	if written > max {
		return 0, ErrTooLarge
	}
	if written == 0 {
		return 0, ErrEmptyUpload
	}
	return written, nil
}

func removeQuietly(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("could not remove a track", "path", path, "err", err)
	}
}

// newToken returns an unguessable URL-safe token.
func newToken() string {
	var raw [24]byte
	if _, err := crand.Read(raw[:]); err != nil {
		panic("tamizchat: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func sortPlaylists(list []storage.Playlist) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j-1].CreatedAt > list[j].CreatedAt; j-- {
			list[j-1], list[j] = list[j], list[j-1]
		}
	}
}
