package httpapi

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"tamizchat/internal/avatars"
	"tamizchat/internal/protocol"
)

// Avatars is the profile picture surface the HTTP layer needs.
type Avatars interface {
	Store(token string, body io.Reader) (clientUUID, version string, err error)
	Open(clientUUID string) (*os.File, string, error)
}

// handleAvatarUpload takes the bytes of a profile picture the client already
// holds a ticket for.
//
// The ticket carries who it belongs to, so this handler needs no separate
// authentication — the same arrangement as a room file and a bot track.
func (d Deps) handleAvatarUpload(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		writeJSON(w, http.StatusUnauthorized,
			errorBody(protocol.ErrBadRequest, "an upload token is required"))
		return
	}

	// A ceiling on the request body regardless of the ticket, so a client cannot
	// stream into this handler forever.
	r.Body = http.MaxBytesReader(w, r.Body, avatars.MaxUpload+(1<<20))

	clientUUID, version, err := d.Avatars.Store(token, r.Body)
	if err != nil {
		status, code, message := avatarError(err)
		writeJSON(w, status, errorBody(code, message))
		return
	}

	// Everybody is told through the socket, because a picture that only appears
	// for people who connect later is not much of a picture.
	if d.OnAvatar != nil {
		d.OnAvatar(clientUUID, version)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"client_uuid": clientUUID,
		"avatar":      version,
	})
}

// handleAvatarFetch serves one user's picture.
//
// Deliberately open, with no token: a profile picture is shown beside a name
// that everyone on the server can already see, and requiring a per-viewer
// ticket would be a round trip that protects nothing. The path is a client id
// and nothing else — the store refuses anything that is not made of the
// characters a UUID is made of, so it cannot name a file outside the folder.
func (d Deps) handleAvatarFetch(w http.ResponseWriter, r *http.Request) {
	file, version, err := d.Avatars.Open(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound,
			errorBody(protocol.ErrFileNotFound, "that user has no profile picture"))
		return
	}
	defer file.Close()

	// The tag is the whole caching story: a client that already has this version
	// is told so and sends nothing over the wire, which is what makes a room of
	// thirty people cost thirty fetches once rather than on every redraw.
	etag := `"` + version + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if info, statErr := file.Stat(); statErr == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", etag)

	// Never sniffed, like every other byte a user uploaded. The store re-encodes
	// what it is given, so this really is a JPEG, but the header costs nothing
	// and the rule is worth keeping unbroken.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")

	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

func avatarError(err error) (int, string, string) {
	switch {
	case errors.Is(err, avatars.ErrBadToken):
		return http.StatusForbidden, protocol.ErrForbidden, "that upload ticket is not valid"
	case errors.Is(err, avatars.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, protocol.ErrFileTooLarge, "that picture is too large"
	case errors.Is(err, avatars.ErrNotImage):
		return http.StatusBadRequest, protocol.ErrFileInvalid, "that file is not an image we can read"
	default:
		return http.StatusInternalServerError, protocol.ErrInternal, "internal server error"
	}
}
