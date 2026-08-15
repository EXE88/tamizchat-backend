package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"tamizchat/internal/bots"
	"tamizchat/internal/protocol"
)

// Bots is what the HTTP layer needs to serve a bot's music to LiveKit, and to
// receive a track uploaded into a playlist.
type Bots interface {
	// ResolveTrack turns a fetch token into the file that is playing now.
	ResolveTrack(botID, token string) (path, title string, err error)
	// UploadTrack consumes an upload ticket and stores the bytes, returning the
	// bot's new state.
	UploadTrack(ctx context.Context, token string, body io.Reader) (protocol.Bot, error)
}

// LiveKitWebhooks verifies and dispatches the callbacks LiveKit sends us.
type LiveKitWebhooks interface {
	HandleWebhook(ctx context.Context, authHeader string, body []byte) error
}

// handleBotStream serves the track a bot is playing.
//
// The only client of this endpoint is LiveKit's Ingress service, which fetches
// the file, transcodes it and publishes it into the room. That is the whole
// reason the audio never passes through this server as media: it leaves here as
// an ordinary file download.
func (d Deps) handleBotStream(w http.ResponseWriter, r *http.Request) {
	botID := r.PathValue("id")
	token := bearerToken(r)

	path, title, err := d.Bots.ResolveTrack(botID, token)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	file, err := os.Open(path)
	if err != nil {
		slog.Error("bot track could not be opened", "bot", botID, "err", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))

	// ServeContent gives Ingress the range requests it uses to seek, and picks
	// the content type from the extension.
	http.ServeContent(w, r, filepath.Base(title+filepath.Ext(path)), info.ModTime(), file)
}

// handleBotTrackUpload receives one track for a playlist. The ticket, issued
// over the WebSocket, already carries the bot, the playlist, the file name and
// the ceiling, so this handler needs no separate authentication — the same
// division of labour as the room-file upload.
func (d Deps) handleBotTrackUpload(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized,
			errorBody(protocol.ErrBadRequest, "an upload token is required"))
		return
	}

	// A hard ceiling on the body regardless of the ticket, so a client cannot
	// stream into this handler forever.
	r.Body = http.MaxBytesReader(w, r.Body, d.MaxTrackBytes()+(1<<20))

	view, err := d.Bots.UploadTrack(r.Context(), token, r.Body)
	if err != nil {
		status, code, message := trackUploadError(err)
		writeJSON(w, status, errorBody(code, message))
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func trackUploadError(err error) (int, string, string) {
	switch {
	case errors.Is(err, bots.ErrBadTicket):
		return http.StatusForbidden, protocol.ErrForbidden, "that upload ticket is not valid any more"
	case errors.Is(err, bots.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, protocol.ErrFileTooLarge, "that track is too large"
	case errors.Is(err, bots.ErrQuotaFull):
		return http.StatusInsufficientStorage, protocol.ErrRoomQuotaFull, "that bot's music storage is full"
	case errors.Is(err, bots.ErrEmptyUpload):
		return http.StatusBadRequest, protocol.ErrFileInvalid, "the upload was empty"
	case errors.Is(err, bots.ErrNotFound), errors.Is(err, bots.ErrPlaylistNotFound):
		return http.StatusNotFound, protocol.ErrPlaylistNotFound, "that playlist no longer exists"
	case errors.Is(err, bots.ErrNotAudio):
		return http.StatusBadRequest, protocol.ErrTrackNotAudio, "that file is not an accepted audio type"
	default:
		slog.Error("bot track upload failed", "err", err)
		return http.StatusInternalServerError, protocol.ErrInternal, "internal server error"
	}
}

// handleLiveKitWebhook receives LiveKit's callbacks — chiefly "this ingress
// ended", which is how a music bot learns its track finished and moves on.
//
// The body is verified against the API secret before anything acts on it: this
// endpoint has no other authentication, so an unsigned request must change
// nothing.
func (d Deps) handleLiveKitWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readLimited(r, 1<<20)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := d.Webhooks.HandleWebhook(r.Context(), r.Header.Get("Authorization"), body); err != nil {
		slog.Warn("rejected livekit webhook", "err", err, "remote", r.RemoteAddr)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
}
