// Command medav is a single-user CalDAV server backed by PostgreSQL.
//
// It performs no authentication of its own: deploy it behind a reverse proxy or
// ingress controller that authenticates requests and terminates TLS. The server
// listens on plain HTTP.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"medav/internal/config"
	"medav/internal/httpx"
	"medav/internal/storage"

	"github.com/emersion/go-webdav/caldav"
)

// Build metadata, stamped via -ldflags by GoReleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))

	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger.Info("starting medav", "version", version, "commit", commit, "date", date, "addr", cfg.ListenAddr)

	// A signal-aware base context aborts in-flight startup work on shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}

	if err := storage.Migrate(ctx, pool); err != nil {
		return err
	}
	logger.Info("migrations applied")

	backend := storage.New(pool, cfg.Prefix)
	if err := backend.EnsureDefaultCalendar(ctx); err != nil {
		return err
	}

	caldavHandler := &caldav.Handler{Backend: backend, Prefix: cfg.Prefix}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpx.New(caldavHandler, logger),
		ReadHeaderTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func logLevel() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
