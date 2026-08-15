// Package app wires the server's components together and owns its lifecycle.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tamizchat/internal/access"
	"tamizchat/internal/bots"
	"tamizchat/internal/chat"
	"tamizchat/internal/config"
	"tamizchat/internal/control"
	"tamizchat/internal/files"
	"tamizchat/internal/gateway"
	"tamizchat/internal/guard"
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

	startedAt := time.Now()
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

	botMgr, err := bots.New(ctx, cfg, store, bots.NewPublisher(mediaMgr), roomMgr, sessions)
	if err != nil {
		return err
	}
	// A deleted room takes its bots out with it, rather than leaving them
	// pointing at a room nobody can join.
	roomMgr.OnDelete(botMgr.RoomGone)

	proxies, err := httpapi.ParseTrustedProxies(cfg.String(config.KeyTrustedProxies))
	if err != nil {
		return fmt.Errorf("trusted proxies: %w", err)
	}
	entryGuard := guard.New(func() guard.Limits {
		return guard.Limits{
			MaxPerIP:  cfg.Int(config.KeyMaxConnsPerIP),
			Burst:     cfg.Int(config.KeyHandshakeBurst),
			PerMinute: cfg.Int(config.KeyHandshakePerMinute),
		}
	})

	gw := gateway.New(cfg, sessions, roomMgr, chatMgr, fileMgr, mediaMgr,
		paintMgr, botMgr, accessMgr, entryGuard, proxies, store, serverUUID)

	handler := httpapi.Handler(httpapi.Deps{
		Config:      cfg,
		ServerUUID:  serverUUID,
		StartedAt:   startedAt,
		OnlineUsers: sessions.Count,
		Gateway:     gw,
		Files:       fileMgr,
		MaxUploadBytes: func() int64 {
			return int64(cfg.Int(config.KeyUploadsMaxSizeMB)) << 20
		},
		OnUpload: gw.AnnounceUpload,
		Bots:     botMgr,
		MaxTrackBytes: func() int64 {
			return int64(cfg.Int(config.KeyBotsMaxTrackMB)) << 20
		},
	})

	addr := cfg.String(config.KeyListenAddr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctrl, err := control.Listen(opts.DBPath, controlHandler{
		cfg:       cfg,
		sessions:  sessions,
		rooms:     roomMgr,
		access:    accessMgr,
		bots:      botMgr,
		media:     mediaMgr,
		startedAt: startedAt,
		pid:       os.Getpid(),
		onRoles:   gw.RefreshRoles,
	})
	if err != nil {
		return err
	}
	defer ctrl.Close()

	slog.Info("tamizchat starting",
		"version", version.Version,
		"addr", addr,
		"tls", cfg.Bool(config.KeyTLSEnabled),
		"db", store.Path(),
		"server_uuid", serverUUID,
		"name", cfg.String(config.KeyServerName),
	)

	// The guard remembers a rate bucket per address; without a sweep a
	// long-running server slowly accumulates one for every address it has met.
	sweeper := time.NewTicker(10 * time.Minute)
	defer sweeper.Stop()
	go func() {
		for range sweeper.C {
			entryGuard.Sweep()
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(srv, cfg)
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

// serve starts the listener, with TLS when it is configured.
//
// TLS is optional on purpose: most operators put this behind nginx or Caddy,
// which already terminate TLS and renew certificates. Building it in is for the
// case where there is nothing in front.
func serve(srv *http.Server, cfg *config.Config) error {
	var err error
	if cfg.Bool(config.KeyTLSEnabled) {
		cert := strings.TrimSpace(cfg.String(config.KeyTLSCertFile))
		key := strings.TrimSpace(cfg.String(config.KeyTLSKeyFile))
		if cert == "" || key == "" {
			return errors.New("TLS is on but the certificate or key path is not set")
		}
		if _, statErr := tls.LoadX509KeyPair(cert, key); statErr != nil {
			return fmt.Errorf("could not read the TLS certificate: %w", statErr)
		}
		// Anything older than TLS 1.2 is broken; there is no client that needs
		// it and every reason not to offer it.
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		err = srv.ListenAndServeTLS(cert, key)
	} else {
		err = srv.ListenAndServe()
	}

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}
	return nil
}
