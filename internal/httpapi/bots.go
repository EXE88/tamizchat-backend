package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// Bots is what the HTTP layer needs to serve a bot's music to LiveKit.
type Bots interface {
	// ResolveTrack turns a fetch token into the file that is playing now.
	ResolveTrack(botID, token string) (path, title string, err error)
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
