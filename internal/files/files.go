// Package files stores what people share in a room: images and other
// attachments.
//
// Like the chat history, uploads are temporary. The bytes sit in a directory on
// disk while the room is alive and are deleted the moment it empties — the room
// registers its file store as a rooms.Ephemeral, so the purge logic from phase 3
// does the work without knowing anything about files. Nothing survives a
// restart either: the upload directory is wiped on startup.
package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"tamizchat/internal/protocol"
)

// Errors the gateway and the HTTP layer map onto protocol error codes.
var (
	ErrDisabled     = errors.New("uploads are disabled")
	ErrForbidden    = errors.New("missing permission")
	ErrNotInRoom    = errors.New("not in a room")
	ErrTooLarge     = errors.New("file is too large")
	ErrQuotaFull    = errors.New("room quota exceeded")
	ErrNotFound     = errors.New("file not found")
	ErrBadTicket    = errors.New("upload ticket is not valid")
	ErrBadToken     = errors.New("download link is not valid")
	ErrEmptyUpload  = errors.New("upload is empty")
	ErrNameRequired = errors.New("file name is required")
)

// ValidationError carries a message meant to be shown to the user as-is.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// File is one stored upload.
type File struct {
	ID        string
	RoomID    string
	Name      string
	Size      int64
	MIME      string
	Kind      string
	Width     int
	Height    int
	HasThumb  bool
	OwnerUUID string
	CreatedAt int64

	path      string
	thumbPath string
}

// Attachment is the public view of the file.
func (f File) Attachment() protocol.Attachment {
	return protocol.Attachment{
		ID:       f.ID,
		Name:     f.Name,
		Size:     f.Size,
		MIME:     f.MIME,
		Kind:     f.Kind,
		Width:    f.Width,
		Height:   f.Height,
		HasThumb: f.HasThumb,
	}
}

// roomStore holds one room's files. It implements rooms.Ephemeral.
type roomStore struct {
	roomID string
	dir    string

	mu    sync.Mutex
	files map[string]*File
	total int64
}

func newRoomStore(roomID, dir string) *roomStore {
	return &roomStore{roomID: roomID, dir: dir, files: make(map[string]*File)}
}

// add records a file and its size against the room's quota.
func (r *roomStore) add(f *File) {
	r.mu.Lock()
	r.files[f.ID] = f
	r.total += f.Size
	r.mu.Unlock()
}

// get looks a file up.
func (r *roomStore) get(id string) (*File, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.files[id]
	return f, ok
}

// used is how many bytes the room currently holds.
func (r *roomStore) used() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// count is how many files the room currently holds.
func (r *roomStore) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.files)
}

// Clear implements rooms.Ephemeral: it deletes the room's files from disk.
// This is the whole point of the design — when the last person leaves, what
// they shared is gone, not merely unreferenced.
func (r *roomStore) Clear() {
	r.mu.Lock()
	files := r.files
	r.files = make(map[string]*File)
	r.total = 0
	dir := r.dir
	r.mu.Unlock()

	for _, f := range files {
		removeQuietly(f.path)
		if f.HasThumb {
			removeQuietly(f.thumbPath)
		}
	}
	// The directory itself goes too, so a long-lived server does not accumulate
	// an empty folder per room it has ever had.
	_ = os.Remove(dir)
}

func removeQuietly(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// wipeDir removes the whole upload directory. Called at startup: every file is
// tied to a live room, and after a restart no room is live.
func wipeDir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear upload dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create upload dir: %w", err)
	}
	return nil
}

// roomDir is where one room's files live.
func roomDir(base, roomID string) string {
	return filepath.Join(base, roomID)
}
