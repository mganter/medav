// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// defaultMaxBodyBytes caps request bodies (e.g. an .ics PUT) so a single client
// cannot exhaust memory by streaming an arbitrarily large body. 10 MiB is far
// above any realistic calendar object.
const defaultMaxBodyBytes int64 = 10 << 20 // 10 MiB

// Config holds all runtime settings. The service is single-user and performs no
// authentication of its own — a reverse proxy / ingress controller is expected
// to authenticate requests upstream.
type Config struct {
	// DatabaseURL is a libpq/pgx connection string, e.g.
	// postgres://user:pass@host:5432/medav?sslmode=disable
	DatabaseURL string
	// ListenAddr is the TCP address the HTTP server binds to. Plain HTTP only;
	// TLS is terminated by the proxy. Defaults to "127.0.0.1:8080" (loopback)
	// so the unauthenticated server is not exposed on all interfaces by default.
	ListenAddr string
	// Prefix is the URL path prefix the CalDAV handler is mounted under. Empty
	// means mounted at the root. It must not end with a trailing slash.
	Prefix string
	// ProxyAuthHeader is the request header the upstream proxy injects to prove
	// a request transited it. Defaults to "X-Medav-Proxy-Auth". Only consulted
	// when ProxyAuthSecret is set.
	ProxyAuthHeader string
	// ProxyAuthSecret, when non-empty, makes the "behind a trusted proxy"
	// assumption enforceable: every request (except /healthz) must carry
	// ProxyAuthHeader with this exact value, otherwise it is rejected with 403.
	// Leave empty only when network isolation (e.g. loopback bind) already
	// guarantees the proxy is the sole client.
	ProxyAuthSecret string
	// MaxBodyBytes caps the size of a request body in bytes. Zero or negative
	// disables the limit. Defaults to defaultMaxBodyBytes.
	MaxBodyBytes int64
}

// Load reads configuration from environment variables, applying defaults.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		ListenAddr:      getenv("LISTEN_ADDR", "127.0.0.1:8080"),
		Prefix:          os.Getenv("PREFIX"),
		ProxyAuthHeader: getenv("PROXY_AUTH_HEADER", "X-Medav-Proxy-Auth"),
		ProxyAuthSecret: os.Getenv("PROXY_AUTH_SECRET"),
		MaxBodyBytes:    defaultMaxBodyBytes,
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	if v := os.Getenv("MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("MAX_BODY_BYTES: %w", err)
		}
		cfg.MaxBodyBytes = n
	}

	// Normalise the prefix: strip a trailing slash so path building stays simple.
	for len(cfg.Prefix) > 0 && cfg.Prefix[len(cfg.Prefix)-1] == '/' {
		cfg.Prefix = cfg.Prefix[:len(cfg.Prefix)-1]
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
