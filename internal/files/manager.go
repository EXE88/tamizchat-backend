package files

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"tamizchat/internal/authz"
	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
	"tamizchat/internal/rooms"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
)

const (
	// ticketTTL is how long a client has to actually send the bytes after
	// asking permission to upload.
	ticketTTL = 2 * time.Minute
	// maxNameLen bounds the display name; it is never used as a path.
	maxNameLen = 120
	// sniffLen is how many bytes the content type is detected from.
	sniffLen = 512
)

// ticket is permission to upload exactly one file, once.
type ticket struct {
	id         string
	token      string
	roomID     string
	clientUUID string
	name       string
	maxSize    int64
	expiresAt  time.Time
}

// grant is permission to download one file for a short while.
type grant struct {
	token     string
	fileID    string
	roomID    string
	expiresAt time.Time
}

// Sessions finds a connected client. session.Manager satisfies it.
type Sessions interface {
	Get(clientUUID string) (*session.Session, bool)
}

// Manager owns every room's files, the upload tickets and the download links.
type Manager struct {
	cfg      *config.Config
	rooms    *rooms.Manager
	sessions Sessions
	policy   authz.Policy

	mu      sync.Mutex
	stores  map[string]*roomStore
	tickets map[string]*ticket
	grants  map[string]*grant
}

// New prepares the upload directory and wires file cleanup into the room
// lifecycle.
func New(cfg *config.Config, roomMgr *rooms.Manager, sessions Sessions,
	policy authz.Policy) (*Manager, error) {
	m := &Manager{
		cfg:      cfg,
		rooms:    roomMgr,
		sessions: sessions,
		policy:   policy,
		stores:   make(map[string]*roomStore),
		tickets:  make(map[string]*ticket),
		grants:   make(map[string]*grant),
	}

	if err := wipeDir(m.baseDir()); err != nil {
		return nil, err
	}

	// A deleted room takes its files with it. The purge path is already
	// handled by the Ephemeral each room store registers.
	roomMgr.OnDelete(func(roomID string) {
		m.mu.Lock()
		store, ok := m.stores[roomID]
		delete(m.stores, roomID)
		m.mu.Unlock()

		if ok {
			store.Clear()
		}
	})

	return m, nil
}

func (m *Manager) baseDir() string { return m.cfg.String(config.KeyUploadsDir) }

func (m *Manager) maxFileSize() int64 {
	return int64(m.cfg.Int(config.KeyUploadsMaxSizeMB)) << 20
}

func (m *Manager) roomQuota() int64 {
	return int64(m.cfg.Int(config.KeyUploadsRoomQuotaMB)) << 20
}

// RequestUpload validates an upload before a single byte is sent and returns
// the ticket the client posts with.
func (m *Manager) RequestUpload(sess *session.Session, req protocol.FileUploadRequest) (protocol.FileUploadTicket, error) {
	if !m.cfg.Bool(config.KeyUploadsEnabled) {
		return protocol.FileUploadTicket{}, ErrDisabled
	}
	if !m.policy.Can(sess.ClientUUID, authz.PermUploadFiles) {
		return protocol.FileUploadTicket{}, ErrForbidden
	}

	roomID := sess.RoomID()
	if roomID == "" {
		return protocol.FileUploadTicket{}, ErrNotInRoom
	}
	if _, ok := m.rooms.Get(roomID); !ok {
		return protocol.FileUploadTicket{}, ErrNotInRoom
	}

	name, err := sanitizeName(req.Name)
	if err != nil {
		return protocol.FileUploadTicket{}, err
	}

	maxSize := m.maxFileSize()
	if req.Size > maxSize {
		return protocol.FileUploadTicket{}, ErrTooLarge
	}
	// A declared size of zero means "I do not know yet"; the hard limit still
	// applies while the bytes stream in.
	if req.Size > 0 && m.store(roomID).used()+req.Size > m.roomQuota() {
		return protocol.FileUploadTicket{}, ErrQuotaFull
	}

	t := &ticket{
		id:         storage.NewUUID(),
		token:      newToken(),
		roomID:     roomID,
		clientUUID: sess.ClientUUID,
		name:       name,
		maxSize:    maxSize,
		expiresAt:  time.Now().Add(ticketTTL),
	}

	m.mu.Lock()
	m.sweepLocked()
	m.tickets[t.token] = t
	m.mu.Unlock()

	return protocol.FileUploadTicket{
		UploadID:  t.id,
		URL:       "/api/v1/upload?token=" + t.token,
		Token:     t.token,
		ExpiresAt: t.expiresAt.Unix(),
		MaxSize:   maxSize,
	}, nil
}

// Upload consumes an upload ticket and stores the bytes. It returns the stored
// file and the uploader's session, which the caller announces in the room.
func (m *Manager) Upload(ctx context.Context, token string, body io.Reader) (File, *session.Session, error) {
	m.mu.Lock()
	m.sweepLocked()
	t, ok := m.tickets[token]
	if ok {
		// A ticket is good for exactly one upload, whether or not it succeeds.
		delete(m.tickets, token)
	}
	m.mu.Unlock()

	if !ok || time.Now().After(t.expiresAt) {
		return File{}, nil, ErrBadTicket
	}

	// The uploader must still be connected and still be in the room they asked
	// about, otherwise the file would land in a room nobody is watching.
	sess, online := m.sessions.Get(t.clientUUID)
	if !online || sess.RoomID() != t.roomID {
		return File{}, nil, ErrNotInRoom
	}
	if _, ok := m.rooms.Get(t.roomID); !ok {
		return File{}, nil, ErrNotInRoom
	}

	store := m.store(t.roomID)
	if err := os.MkdirAll(store.dir, 0o750); err != nil {
		return File{}, nil, fmt.Errorf("create room dir: %w", err)
	}

	fileID := storage.NewUUID()
	path := filepath.Join(store.dir, fileID)

	size, err := writeLimited(path, body, t.maxSize)
	if err != nil {
		removeQuietly(path)
		return File{}, nil, err
	}
	if remaining := m.roomQuota() - store.used(); size > remaining {
		removeQuietly(path)
		return File{}, nil, ErrQuotaFull
	}

	file := &File{
		ID:        fileID,
		RoomID:    t.roomID,
		Name:      t.name,
		Size:      size,
		OwnerUUID: t.clientUUID,
		CreatedAt: time.Now().Unix(),
		path:      path,
	}

	// The declared content type is ignored: only the bytes are trusted.
	file.MIME = detectMIME(path)
	file.Kind = protocol.AttachmentFile
	if isImageMIME(file.MIME) {
		file.Kind = protocol.AttachmentImage
		thumbPath := path + ".thumb"
		w, h, thumbed := makeThumbnail(path, thumbPath, m.cfg.Int(config.KeyUploadsThumbMaxPx))
		file.Width, file.Height = w, h
		if thumbed {
			file.HasThumb = true
			file.thumbPath = thumbPath
		}
	}

	store.add(file)
	slog.Info("file stored", "room", t.roomID, "file", file.ID, "name", file.Name,
		"size", file.Size, "mime", file.MIME, "by", t.clientUUID)
	return *file, sess, nil
}

// IssueDownload hands out a short-lived link to a file in the caller's room.
func (m *Manager) IssueDownload(sess *session.Session, fileID string) (protocol.FileDownload, error) {
	roomID := sess.RoomID()
	if roomID == "" {
		return protocol.FileDownload{}, ErrNotInRoom
	}

	// A download link only works for the room the caller is currently in, so a
	// file id leaked from one room cannot be fetched from another.
	store := m.store(roomID)
	file, ok := store.get(fileID)
	if !ok {
		return protocol.FileDownload{}, ErrNotFound
	}

	ttl := time.Duration(m.cfg.Int(config.KeyUploadsTokenTTLSec)) * time.Second
	g := &grant{
		token:     newToken(),
		fileID:    fileID,
		roomID:    roomID,
		expiresAt: time.Now().Add(ttl),
	}

	m.mu.Lock()
	m.sweepLocked()
	m.grants[g.token] = g
	m.mu.Unlock()

	out := protocol.FileDownload{
		FileID:    fileID,
		URL:       "/api/v1/file/" + fileID + "?token=" + g.token,
		ExpiresAt: g.expiresAt.Unix(),
	}
	if file.HasThumb {
		out.ThumbURL = out.URL + "&thumb=1"
	}
	return out, nil
}

// Open resolves a download token and returns the file's contents. The caller
// closes the returned reader.
func (m *Manager) Open(fileID, token string, thumb bool) (File, *os.File, error) {
	m.mu.Lock()
	m.sweepLocked()
	g, ok := m.grants[token]
	m.mu.Unlock()

	if !ok || g.fileID != fileID || time.Now().After(g.expiresAt) {
		return File{}, nil, ErrBadToken
	}

	file, ok := m.store(g.roomID).get(fileID)
	if !ok {
		return File{}, nil, ErrNotFound
	}

	path := file.path
	if thumb {
		if !file.HasThumb {
			return File{}, nil, ErrNotFound
		}
		path = file.thumbPath
	}

	f, err := os.Open(path)
	if err != nil {
		// The room was purged between issuing the link and following it.
		return File{}, nil, ErrNotFound
	}
	return *file, f, nil
}

// RoomUsage reports how much of a room's quota is in use, for the panel and
// for tests.
func (m *Manager) RoomUsage(roomID string) (used int64, count int) {
	store := m.store(roomID)
	return store.used(), store.count()
}

// store returns the room's file store, creating and registering it on first
// use. Registering it as an Ephemeral is what ties file deletion to the room
// emptying.
func (m *Manager) store(roomID string) *roomStore {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.stores[roomID]; ok {
		return s
	}
	s := newRoomStore(roomID, roomDir(m.baseDir(), roomID))
	m.stores[roomID] = s

	if room, ok := m.rooms.Get(roomID); ok {
		room.AddEphemeral(s)
	}
	return s
}

// sweepLocked drops expired tickets and grants. The maps are small, so a linear
// pass on each operation is cheaper than a background goroutine. The caller
// must hold m.mu.
func (m *Manager) sweepLocked() {
	now := time.Now()
	for token, t := range m.tickets {
		if now.After(t.expiresAt) {
			delete(m.tickets, token)
		}
	}
	for token, g := range m.grants {
		if now.After(g.expiresAt) {
			delete(m.grants, token)
		}
	}
}

// writeLimited streams body to path, refusing to exceed max bytes.
func writeLimited(path string, body io.Reader, max int64) (int64, error) {
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer dst.Close()

	// Read one byte past the limit: if it arrives, the file is too big.
	written, err := io.Copy(dst, io.LimitReader(body, max+1))
	if err != nil {
		return 0, fmt.Errorf("write file: %w", err)
	}
	if written > max {
		return 0, ErrTooLarge
	}
	if written == 0 {
		return 0, ErrEmptyUpload
	}
	return written, nil
}

// detectMIME sniffs the stored bytes. The client's claim is never used: a
// mislabelled file would otherwise decide how another client renders it.
func detectMIME(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()

	head := make([]byte, sniffLen)
	n, _ := io.ReadFull(f, head)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(head[:n])
}

func isImageMIME(mime string) bool {
	return strings.HasPrefix(mime, "image/")
}

// sanitizeName cleans a filename for display. It is never used to build a path
// — files are stored under a random id — but it still must not contain path
// separators or control characters that would confuse a client saving it.
func sanitizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)

	if name == "" || name == "." || name == ".." || name == "/" {
		return "", ErrNameRequired
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r), r == '/', r == 0:
			continue
		default:
			b.WriteRune(r)
		}
	}
	name = strings.TrimSpace(b.String())
	if name == "" {
		return "", ErrNameRequired
	}

	if runes := []rune(name); len(runes) > maxNameLen {
		// Keep the extension, which is the part a person recognises.
		ext := filepath.Ext(name)
		keep := maxNameLen - len([]rune(ext))
		if keep < 1 {
			keep, ext = maxNameLen, ""
		}
		name = string(runes[:keep]) + ext
	}
	return name, nil
}

// newToken returns an unguessable URL-safe token.
func newToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("tamizchat: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
