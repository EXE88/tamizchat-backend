// Package avatars stores the profile picture a user chose for themselves.
//
// A picture is not room content: it belongs to the person, not to a
// conversation, so unlike everything in internal/files it is permanent, it
// survives a purge, and it is not scoped to a room. That is the whole reason
// this is a package of its own rather than another kind of upload.
//
// What is stored is always a square JPEG at a fixed size, re-encoded here from
// whatever the client sent. Three things fall out of that and all three are the
// point: an operator's disk cannot be filled by somebody uploading a 60
// megapixel photograph, a client cannot smuggle an SVG or an HTML file past the
// extension check because the bytes are decoded and written out again, and
// every client renders the same picture at the same size.
package avatars

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // registers the GIF decoder
	"image/jpeg"
	_ "image/png" // registers the PNG decoder
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Size is the side of the stored square, in pixels.
//
// 256 is comfortably more than the largest circle any client draws — the room
// grid tops out at 96 — with room for a bigger view later, and it is about 15 kB
// of JPEG rather than the megabytes a phone camera produces.
const Size = 256

// MaxUpload is the largest picture accepted, before decoding.
const MaxUpload = 8 << 20

// Errors a caller is expected to tell apart.
var (
	ErrBadToken = errors.New("avatars: unknown or spent upload ticket")
	ErrTooLarge = errors.New("avatars: the picture is too large")
	ErrNotImage = errors.New("avatars: that is not an image we can read")
	ErrNoAvatar = errors.New("avatars: this user has no picture")
)

// ticketTTL is how long an upload ticket is good for. Short: it is spent within
// a second of being issued by any client that is actually uploading.
const ticketTTL = 2 * time.Minute

type ticket struct {
	clientUUID string
	expires    time.Time
}

// Manager owns the picture folder and the outstanding upload tickets.
type Manager struct {
	dir string

	mu      sync.Mutex
	tickets map[string]ticket

	// versions is the current picture's tag per user, kept in memory because it
	// is read on every user the server describes and written once in a while.
	// It is rebuilt from the folder at startup.
	versions map[string]string
}

// New opens or creates the picture folder.
func New(dir string) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("avatars: create %s: %w", dir, err)
	}

	m := &Manager{
		dir:      dir,
		tickets:  map[string]ticket{},
		versions: map[string]string{},
	}
	m.scan()
	return m, nil
}

// scan rebuilds the version map from what is on disk.
//
// The tag is derived from the file's modification time rather than stored
// anywhere: it only has to change when the picture changes, and a timestamp
// does that for free. Keeping it in the database would be a second source of
// truth that a restored backup or a hand-copied folder could disagree with.
func (m *Manager) scan() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jpg" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		uuid := entry.Name()[:len(entry.Name())-len(".jpg")]
		m.versions[uuid] = tag(info.ModTime())
	}
}

func tag(t time.Time) string { return fmt.Sprintf("%x", t.UnixNano()) }

// Version is the current picture's tag for one user, or "" when they have none.
//
// Clients use it to cache: the same tag means the same picture, so a room full
// of people costs one fetch each and nothing after that. It is also what makes
// a *changed* picture appear without anyone reconnecting — the tag in the user
// record is different, so the client knows to fetch again.
func (m *Manager) Version(clientUUID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.versions[clientUUID]
}

// Ticket issues a single-use permission to upload a picture, the same pattern
// as room files and bot tracks: everything that can be refused is refused
// before a byte is sent.
func (m *Manager) Ticket(clientUUID string) string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	token := hex.EncodeToString(raw)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Sweep here rather than on a timer: this map only grows when somebody asks
	// to upload, so the moment somebody asks is exactly when it is worth
	// tidying.
	now := time.Now()
	for key, value := range m.tickets {
		if now.After(value.expires) {
			delete(m.tickets, key)
		}
	}

	m.tickets[token] = ticket{clientUUID: clientUUID, expires: now.Add(ticketTTL)}
	return token
}

// Store decodes the uploaded picture, squares it, scales it and writes it.
//
// It returns the user it belongs to and the new version tag, so the caller can
// announce the change to everybody.
func (m *Manager) Store(token string, body io.Reader) (clientUUID, version string, err error) {
	m.mu.Lock()
	found, ok := m.tickets[token]
	delete(m.tickets, token)
	m.mu.Unlock()

	if !ok || time.Now().After(found.expires) {
		return "", "", ErrBadToken
	}

	// Read with a hard ceiling. A decoder that is handed an endless stream will
	// happily keep allocating.
	raw, err := io.ReadAll(io.LimitReader(body, MaxUpload+1))
	if err != nil {
		return "", "", err
	}
	if len(raw) > MaxUpload {
		return "", "", ErrTooLarge
	}

	img, _, err := decode(raw)
	if err != nil {
		return "", "", ErrNotImage
	}

	square := squareAndScale(img, Size)

	path := m.path(found.clientUUID)
	temp := path + ".tmp"

	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", "", err
	}

	if err := jpeg.Encode(file, square, &jpeg.Options{Quality: 88}); err != nil {
		_ = file.Close()
		_ = os.Remove(temp)
		return "", "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temp)
		return "", "", err
	}

	// Written beside and moved into place, so a picture is never half replaced
	// while somebody is fetching it.
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return "", "", err
	}

	now := time.Now()
	_ = os.Chtimes(path, now, now)
	version = tag(now)

	m.mu.Lock()
	m.versions[found.clientUUID] = version
	m.mu.Unlock()

	return found.clientUUID, version, nil
}

// Remove deletes a user's picture. Removing one they do not have is not an
// error: the caller wanted them to have none, and they have none.
func (m *Manager) Remove(clientUUID string) {
	_ = os.Remove(m.path(clientUUID))

	m.mu.Lock()
	delete(m.versions, clientUUID)
	m.mu.Unlock()
}

// Open returns the stored picture for reading.
func (m *Manager) Open(clientUUID string) (*os.File, string, error) {
	if !validUUID(clientUUID) {
		return nil, "", ErrNoAvatar
	}

	file, err := os.Open(m.path(clientUUID))
	if err != nil {
		return nil, "", ErrNoAvatar
	}
	return file, m.Version(clientUUID), nil
}

func (m *Manager) path(clientUUID string) string {
	return filepath.Join(m.dir, clientUUID+".jpg")
}

// validUUID keeps a client id out of the filename unless it is made only of the
// characters a UUID is made of.
//
// This is the whole of the path safety here, and it is enough precisely because
// it is a whitelist: no dot, no separator and no colon can survive it, so
// nothing a caller sends can climb out of the folder or name a stream.
func validUUID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F', r == '-':
		default:
			return false
		}
	}
	return true
}

func decode(raw []byte) (image.Image, string, error) {
	return image.Decode(newReader(raw))
}
