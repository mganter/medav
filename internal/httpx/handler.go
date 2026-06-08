// Package httpx assembles the public HTTP surface: the CalDAV handler plus a
// liveness endpoint and lightweight request logging. TLS and authentication are
// handled upstream by a reverse proxy, so neither appears here — but the
// middleware below lets that proxy dependency be enforced (a shared secret) and
// hardens the surface (body-size cap, security headers).
package httpx

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// healthzPath is exempt from the proxy-auth check so orchestrator liveness
// probes (which do not carry the proxy secret) keep working.
const healthzPath = "/healthz"

// Options configures the HTTP surface assembled by New.
type Options struct {
	// ProxyAuthHeader/ProxyAuthSecret: when Secret is non-empty, every request
	// except /healthz must carry Header with exactly Secret, else it is rejected
	// with 403. This makes the "behind a trusted proxy" assumption enforceable.
	ProxyAuthHeader string
	ProxyAuthSecret string
	// MaxBodyBytes caps the request body size. Zero or negative disables it.
	MaxBodyBytes int64
}

// New returns the root http.Handler. The CalDAV handler is mounted at "/" so it
// receives full request paths (it matches "/.well-known/caldav" itself and
// redirects to the principal). "/healthz" is reserved for liveness probes.
//
// Middleware is applied outermost-first: logging → security headers →
// proxy-auth → body limit → mux, so rejected requests are logged and never
// reach the backend with an oversized body.
func New(caldavHandler http.Handler, logger *slog.Logger, opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(healthzPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", caldavHandler)

	var h http.Handler = mux
	if opts.MaxBodyBytes > 0 {
		h = limitBody(h, opts.MaxBodyBytes)
	}
	if opts.ProxyAuthSecret != "" {
		h = requireProxyAuth(h, opts.ProxyAuthHeader, opts.ProxyAuthSecret)
	}
	h = secureHeaders(h)
	return logging(h, logger)
}

// requireProxyAuth rejects any request (except liveness probes) that does not
// carry the expected shared secret, proving it arrived via the trusted proxy.
// The comparison is constant-time to avoid leaking the secret via timing.
func requireProxyAuth(next http.Handler, header, secret string) http.Handler {
	want := []byte(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthzPath {
			got := []byte(r.Header.Get(header))
			if subtle.ConstantTimeCompare(got, want) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody caps the request body so a single client cannot exhaust memory by
// streaming an arbitrarily large body; reads past the cap fail with 413.
func limitBody(next http.Handler, max int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}

// secureHeaders sets conservative security response headers. The service serves
// CalDAV/iCalendar data (no HTML/JS), so nosniff is the most relevant control;
// HSTS is intentionally left to the TLS-terminating proxy.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// logging records one line per request. Successful, frequently-polled requests
// stay at debug to avoid noise, but access failures (4xx) and server errors
// (5xx) are logged at warn/error so they are visible at the default info level.
// The request path is attacker-controlled, so it is sanitized first (CWE-117).
func logging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)

		level := slog.LevelDebug
		switch {
		case sw.status >= 500:
			level = slog.LevelError
		case sw.status >= 400:
			level = slog.LevelWarn
		}
		// Record the source so access-control failures (e.g. a 403 from the
		// proxy-auth gate) are attributable. RemoteAddr is the immediate peer
		// (the proxy when deployed as intended); the proxy-supplied
		// X-Forwarded-For, when present, carries the real client. It is
		// client-controlled, so it is sanitized like the path (CWE-117).
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", sanitizeForLog(r.URL.Path)),
			slog.Int("status", sw.status),
			slog.Duration("duration", time.Since(start)),
			slog.String("remote", r.RemoteAddr),
		}
		if ff := r.Header.Get("X-Forwarded-For"); ff != "" {
			attrs = append(attrs, slog.String("forwarded_for", sanitizeForLog(ff)))
		}
		logger.LogAttrs(r.Context(), level, "request", attrs...)
	})
}

// sanitizeForLog strips control characters (including CR/LF) from a
// user-controlled string so it cannot forge or split log lines (CWE-117).
func sanitizeForLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
