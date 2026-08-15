package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"tamizchat/internal/bots"
	"tamizchat/internal/protocol"
)

// Bots is what the HTTP layer needs from the bot engine: somewhere to put an
// uploaded track. The music itself never travels over HTTP — a bot publishes it
// into LiveKit directly, as a participant.
type Bots interface {
	// UploadTrack consumes an upload ticket and stores the bytes, returning the
	// bot's new state.
	UploadTrack(ctx context.Context, token string, body io.Reader) (protocol.Bot, error)
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
	case errors.Is(err, bots.ErrNotOpus):
		return http.StatusBadRequest, protocol.ErrTrackNotAudio, "a bot plays Ogg/Opus only"
	default:
		slog.Error("bot track upload failed", "err", err)
		return http.StatusInternalServerError, protocol.ErrInternal, "internal server error"
	}
}
