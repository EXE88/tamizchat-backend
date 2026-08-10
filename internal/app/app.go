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

	"tamizchat/internal/config"
	"tamizchat/internal/httpapi"
	"tamizchat/internal/logging"
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

	handler := httpapi.Handler(httpapi.Deps{
		Config:      cfg,
		ServerUUID:  serverUUID,
		StartedAt:   time.Now(),
		OnlineUsers: func() int { return 0 }, // replaced by the session manager in phase 3
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
		slog.Info("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("stopped")
	return nil
}
