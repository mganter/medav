// Package httpx assembles the public HTTP surface: the CalDAV handler plus a
// liveness endpoint and lightweight request logging. TLS and authentication are
// handled upstream by a reverse proxy, so neither appears here.
package httpx

import (
	"log/slog"
	"net/http"
	"time"
)

// New returns the root http.Handler. The CalDAV handler is mounted at "/" so it
// receives full request paths (it matches "/.well-known/caldav" itself and
// redirects to the principal). "/healthz" is reserved for liveness probes.
func New(caldavHandler http.Handler, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", caldavHandler)
	return logging(mux, logger)
}

// logging records one line per request at debug level. CalDAV clients poll
// frequently, so info-level logging of every request would be noisy.
func logging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		logger.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
