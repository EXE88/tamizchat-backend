package bots

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/storage"
	"tamizchat/internal/textutil"
)

// maxBots bounds how many bots a server can hold. Each one is a participant
// LiveKit has to carry, and a client renders every one of them in the room
// tree; a thousand bots is a mistake, not a use case.
const maxBots = 64

// ErrTooManyBots is returned when the server is already at maxBots.
var ErrTooManyBots = errors.New("too many bots")

// ErrNameTaken is returned when another bot already uses that name.
var ErrNameTaken = errors.New("bot name already used")

// Create defines a new bot and gives it a folder of its own.
//
// The folder is derived from the bot's id rather than taken from the caller:
// this path is opened by the server, so letting a client name it would turn bot
// creation into a way to read any directory on the host through the stream
// endpoint.
func (m *Manager) Create(ctx context.Context, actorUUID string, spec protocol.BotSpec) (protocol.Bot, error) {
	if spec.Name == nil {
		return protocol.Bot{}, &ValidationError{Msg: "the bot name is required"}
	}
	name, err := textutil.NormalizeName(*spec.Name, "bot name", 1, 32)
	if err != nil {
		return protocol.Bot{}, &ValidationError{Msg: err.Error()}
	}
	color, err := normalizeColor(spec.Color)
	if err != nil {
		return protocol.Bot{}, err
	}

	m.mu.Lock()
	if len(m.bots) >= maxBots {
		m.mu.Unlock()
		return protocol.Bot{}, ErrTooManyBots
	}
	m.mu.Unlock()

	def := storage.Bot{
		ID:        storage.NewUUID(),
		Name:      name,
		Kind:      storage.BotKindMusic,
		Color:     color,
		LoopQueue: true,
		Enabled:   true,
	}
	// The bot's own library is a folder *inside* its storage, not the storage
	// root: playlists are siblings of it, and a scan of the library walks
	// subfolders, so a nested playlist would show up twice.
	def.Folder = m.libraryFolder(def.ID)
	if spec.Loop != nil {
		def.LoopQueue = *spec.Loop
	}
	if spec.Shuffle != nil {
		def.Shuffle = *spec.Shuffle
	}
	if spec.Enabled != nil {
		def.Enabled = *spec.Enabled
	}

	// The folder comes first: a bot whose folder could not be created would be a
	// row that never plays anything, and the error would surface later with no
	// obvious cause.
	if err := os.MkdirAll(def.Folder, 0o755); err != nil {
		return protocol.Bot{}, fmt.Errorf("create bot folder: %w", err)
	}
	if err := m.store.CreateBot(ctx, def); err != nil {
		if errors.Is(err, storage.ErrBotNameTaken) {
			return protocol.Bot{}, ErrNameTaken
		}
		return protocol.Bot{}, err
	}

	m.mu.Lock()
	b := &bot{def: def, state: protocol.BotIdle}
	m.bots[def.ID] = b
	view := m.viewLocked(b)
	m.mu.Unlock()

	m.announce(actorUUID, view)
	slog.Info("bot created", "bot", def.Name, "folder", def.Folder)
	return view, nil
}

// Update changes a bot's editable fields. A nil field is left alone.
func (m *Manager) Update(ctx context.Context, actorUUID string, spec protocol.BotSpec) (protocol.Bot, error) {
	m.mu.Lock()
	b, ok := m.bots[spec.BotID]
	if !ok {
		m.mu.Unlock()
		return protocol.Bot{}, ErrNotFound
	}
	def := b.def
	m.mu.Unlock()

	if spec.Name != nil {
		name, err := textutil.NormalizeName(*spec.Name, "bot name", 1, 32)
		if err != nil {
			return protocol.Bot{}, &ValidationError{Msg: err.Error()}
		}
		def.Name = name
	}
	if spec.Color != nil {
		color, err := normalizeColor(spec.Color)
		if err != nil {
			return protocol.Bot{}, err
		}
		def.Color = color
	}
	if spec.Loop != nil {
		def.LoopQueue = *spec.Loop
	}
	if spec.Shuffle != nil {
		def.Shuffle = *spec.Shuffle
	}
	if spec.Enabled != nil {
		def.Enabled = *spec.Enabled
	}

	if err := m.store.UpdateBot(ctx, def); err != nil {
		switch {
		case errors.Is(err, storage.ErrBotNameTaken):
			return protocol.Bot{}, ErrNameTaken
		case errors.Is(err, storage.ErrBotNotFound):
			return protocol.Bot{}, ErrNotFound
		}
		return protocol.Bot{}, err
	}

	// Disabling a bot that is mid-track has to actually silence it, or the
	// switch says "off" while the room still hears music.
	if !def.Enabled {
		m.mu.Lock()
		b.def = def
		m.mu.Unlock()
		view, err := m.stop(ctx, actorUUID, def.ID)
		if err != nil {
			return protocol.Bot{}, err
		}
		return view, nil
	}

	m.mu.Lock()
	b.def = def
	view := m.viewLocked(b)
	m.mu.Unlock()

	m.announce(actorUUID, view)
	return view, nil
}

// Delete removes a bot: it stops playing, leaves its room and is gone from the
// database.
//
// Its playlists go with it, and so does the folder this server made for it. A
// folder an operator typed into the CLI panel is theirs, may hold music that
// belongs to something else entirely, and is left exactly as it was.
func (m *Manager) Delete(ctx context.Context, botID string) error {
	m.mu.Lock()
	b, ok := m.bots[botID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	ingressID := b.ingressID
	name := b.def.Name
	m.dropGrantsLocked(botID)
	m.mu.Unlock()

	// A row that has already gone is not a failure: the caller asked for this
	// bot to stop existing, and it does not. Refusing here used to leave a bot
	// that had been deleted from the panel without a reload stuck in memory and
	// impossible to remove from a client — the delete failed on the missing row
	// before it ever reached the map.
	if err := m.store.DeleteBot(ctx, botID); err != nil && !errors.Is(err, storage.ErrBotNotFound) {
		return err
	}
	if err := m.store.DeletePlaylistsOfBot(ctx, botID); err != nil {
		return err
	}

	m.mu.Lock()
	delete(m.bots, botID)
	for id, def := range m.playlists {
		if def.BotID == botID {
			delete(m.playlists, id)
		}
	}
	m.mu.Unlock()

	m.stopIngress(ctx, ingressID)

	// Only the folder we made — which is where every playlist lives — is
	// removed. It is ours by construction, so there is nothing of anyone else's
	// in it.
	if err := os.RemoveAll(m.managedFolder(botID)); err != nil {
		slog.Warn("could not remove a deleted bot's folder",
			"bot", name, "folder", m.managedFolder(botID), "err", err)
	}

	slog.Info("bot deleted", "bot", name)
	return nil
}

// libraryFolder is what a client-created bot plays from when no playlist is
// selected — its own loose collection, alongside its playlists.
func (m *Manager) libraryFolder(botID string) string {
	return filepath.Join(m.managedFolder(botID), "default")
}

// managedFolder is the storage this server owns for one bot: its library, its
// playlists, and nothing of anybody else's. It is also the test for whether a
// folder is ours to delete.
func (m *Manager) managedFolder(botID string) string {
	return filepath.Join(m.storageRoot(), botID)
}

// storageRoot is where every bot's music lives.
//
// A relative setting — "bots", the default — is resolved **next to the
// database**, not against the working directory. A server is started by systemd
// or a container from whatever directory they please, and music that is
// permanent must not land somewhere that depends on that: in the dev container
// it ended up in /data/data/bots, and one `WorkingDirectory=` away it would
// have been outside the data volume altogether.
func (m *Manager) storageRoot() string {
	root := strings.TrimSpace(m.cfg.String(config.KeyBotsDir))
	if root == "" {
		root = "bots"
	}
	if filepath.IsAbs(root) || m.store == nil {
		return root
	}
	return filepath.Join(filepath.Dir(m.store.Path()), root)
}

// normalizeColor keeps a colour short enough to be a colour. It is not parsed:
// the client decides what it means, exactly as it does for a role.
func normalizeColor(color *string) (string, error) {
	if color == nil {
		return "", nil
	}
	trimmed := strings.TrimSpace(*color)
	if len(trimmed) > 32 {
		return "", &ValidationError{Msg: "that colour value is too long"}
	}
	return trimmed, nil
}
