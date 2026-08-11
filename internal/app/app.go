// Package app wires the server's components together and owns its lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"tamizchat/internal/access"
	"tamizchat/internal/bots"
	"tamizchat/internal/chat"
	"tamizchat/internal/config"
	"tamizchat/internal/files"
	"tamizchat/internal/gateway"
	"tamizchat/internal/httpapi"
	"tamizchat/internal/logging"
	"tamizchat/internal/media"
	"tamizchat/internal/paint"
	"tamizchat/internal/protocol"
	"tamizchat/internal/rooms"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
	"tamizchat/internal/version"
)

// Options are the few knobs that cannot live in the database, because they are
// needed before the database is open.
type Options struct {
	DBPath string
}

// Run starts the server and blocks until SIGINT/SIGTERM, then shuts down.
func Run(ctx context.Context, opts Options) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, opts.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	cfg, err := config.Load(ctx, store)
	if err != nil {
		return err
	}
	logging.SetLevel(cfg.String(config.KeyLogLevel))
	cfg.Watch(func(key, value string) {
		if key == config.KeyLogLevel {
			logging.SetLevel(value)
			slog.Info("log level changed", "level", value)
		}
	})

	serverUUID, err := store.ServerUUID(ctx)
	if err != nil {
		return err
	}

	accessMgr, err := access.New(ctx, store)
	if err != nil {
		return err
	}

	sessions := session.NewManager(func() int { return cfg.Int(config.KeyServerMaxUsers) })
	roomMgr, err := rooms.NewManager(ctx, store, cfg, sessions, accessMgr)
	if err != nil {
		return err
	}
	chatMgr := chat.NewManager(cfg, roomMgr, accessMgr, accessMgr)
	fileMgr, err := files.New(cfg, roomMgr, sessions, accessMgr)
	if err != nil {
		return err
	}

	mediaMgr := media.New(cfg, accessMgr, accessMgr)
	// Leaving a room ends the media session for it, whatever the reason:
	// leaving on purpose, switching rooms, being moved, or disconnecting.
	roomMgr.OnMemberLeft(mediaMgr.Disconnect)
	roomMgr.OnDelete(mediaMgr.CloseRoom)

	paintMgr := paint.NewManager(cfg, roomMgr, accessMgr, accessMgr)

	botMgr, err := bots.New(ctx, cfg, store, mediaMgr, roomMgr, sessions)
	if err != nil {
		return err
	}
	// A deleted room takes its bots out with it, rather than leaving them
	// pointing at a room nobody can join.
	roomMgr.OnDelete(botMgr.RoomGone)

	gw := gateway.New(cfg, sessions, roomMgr, chatMgr, fileMgr, mediaMgr,
		paintMgr, botMgr, accessMgr, store, serverUUID)

	handler := httpapi.Handler(httpapi.Deps{
		Config:      cfg,
		ServerUUID:  serverUUID,
		StartedAt:   time.Now(),
		OnlineUsers: sessions.Count,
		Gateway:     gw,
		Files:       fileMgr,
		MaxUploadBytes: func() int64 {
			return int64(cfg.Int(config.KeyUploadsMaxSizeMB)) << 20
		},
		OnUpload: gw.AnnounceUpload,
		Bots:     botMgr,
		Webhooks: webhookRouter{media: mediaMgr, bots: botMgr},
	})

	addr := cfg.String(config.KeyListenAddr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	slog.Info("tamizchat starting",
		"version", version.Version,
		"addr", addr,
		"db", store.Path(),
		"server_uuid", serverUUID,
		"name", cfg.String(config.KeyServerName),
	)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen on %s: %w", addr, err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutdown requested", "online", sessions.Count())
	}

	// Tell clients why they are being disconnected before the listener closes,
	// so they can show a proper message instead of a generic network error.
	sessions.CloseAll(protocol.ReasonShutdown)

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("stopped")
	return nil
}
