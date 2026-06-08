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
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"medav/internal/caldavx"
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

	// medav authenticates nothing itself; it trusts every request as the single
	// user. That is safe only when the upstream proxy is the sole reachable
	// client. Warn loudly if neither network isolation (loopback bind) nor a
	// shared proxy secret enforces that assumption.
	if !isLoopbackAddr(cfg.ListenAddr) && cfg.ProxyAuthSecret == "" {
		logger.Warn("listening on a non-loopback address with no PROXY_AUTH_SECRET set: "+
			"medav performs no authentication and trusts every request. "+
			"Bind to loopback or set PROXY_AUTH_SECRET so only the upstream proxy can reach it.",
			"addr", cfg.ListenAddr)
	}

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

	backend := storage.New(pool, cfg.Prefix, cfg.MaxCalendars, cfg.MaxObjectsPerCalendar, logger)
	if err := backend.EnsureDefaultCalendar(ctx); err != nil {
		return err
	}

	caldavHandler := &caldav.Handler{Backend: backend, Prefix: cfg.Prefix}
	// go-webdav has no MKCALENDAR; wrap it so standard clients can create calendars.
	root := caldavx.Wrap(caldavHandler, backend, storage.NewPaths(cfg.Prefix))
	handler := httpx.New(root, logger, httpx.Options{
		ProxyAuthHeader: cfg.ProxyAuthHeader,
		ProxyAuthSecret: cfg.ProxyAuthSecret,
		MaxBodyBytes:    cfg.MaxBodyBytes,
	})
	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,
		// Timeouts bound how long a single connection can tie up resources,
		// defending against slowloris / slow-body attacks. ReadTimeout covers
		// reading the (size-capped) body; WriteTimeout is generous for large
		// REPORT responses.
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
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

// isLoopbackAddr reports whether addr binds only to the loopback interface. An
// empty host (e.g. ":8080") means all interfaces and is treated as non-loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "":
		return false // all interfaces
	case "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
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
