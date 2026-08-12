// Package httpapi exposes the server's HTTP surface. Phase 1 only serves the
// discovery and health endpoints a client needs before it decides to connect;
// the WebSocket gateway and REST resources arrive in later phases.
package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/files"
	"tamizchat/internal/session"
	"tamizchat/internal/version"
)

// Deps is everything the HTTP layer needs from the rest of the server.
type Deps struct {
	Config     *config.Config
	ServerUUID string
	StartedAt  time.Time
	// OnlineUsers reports the current session count.
	OnlineUsers func() int
	// Gateway handles the WebSocket upgrade at /ws.
	Gateway http.Handler
	// Files serves uploads and downloads. Nil disables both endpoints.
	Files Files
	// MaxUploadBytes is the current per-file ceiling.
	MaxUploadBytes func() int64
	// OnUpload announces a finished upload in the uploader's room.
	OnUpload func(uploader *session.Session, file files.File)
	// Bots serves the audio a music bot is playing. Nil disables the endpoint.
	Bots Bots
	// Webhooks verifies LiveKit's callbacks. Nil disables the endpoint.
	Webhooks LiveKitWebhooks
}

// Handler builds the router.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "ok",
			"uptime_sec": int(time.Since(d.StartedAt).Seconds()),
		})
	})

	// Called by a client before joining, so it can show the server name and
	// whether a password is required. Nothing secret is exposed here.
	mux.HandleFunc("GET /api/v1/server-info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"server_uuid":       d.ServerUUID,
			"name":              d.Config.String(config.KeyServerName),
			"welcome":           d.Config.String(config.KeyServerWelcome),
			"protocol_version":  ProtocolVersion,
			"software_version":  version.Version,
			"password_required": d.Config.String(config.KeyServerPassword) != "",
			"max_users":         d.Config.Int(config.KeyServerMaxUsers),
			"online_users":      d.OnlineUsers(),
			"media_enabled":     d.Config.Bool(config.KeyLiveKitEnabled),
			"uploads_enabled":   d.Config.Bool(config.KeyUploadsEnabled),
			"max_upload_mb":     d.Config.Int(config.KeyUploadsMaxSizeMB),
		})
	})

	if d.Gateway != nil {
		mux.Handle("/ws", d.Gateway)
	}

	if d.Files != nil {
		mux.HandleFunc("POST /api/v1/upload", d.handleUpload)
		mux.HandleFunc("GET /api/v1/file/{id}", d.handleDownload)
	}

	if d.Bots != nil {
		mux.HandleFunc("GET /api/v1/bot-stream/{id}", d.handleBotStream)
	}
	if d.Webhooks != nil {
		mux.HandleFunc("POST /api/v1/livekit/webhook", d.handleLiveKitWebhook)
	}

	return recoverPanics(secureHeaders(logRequests(mux)))
}

// ProtocolVersion is bumped whenever the client/server wire contract changes in
// a way older clients cannot handle.
const ProtocolVersion = 1

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
