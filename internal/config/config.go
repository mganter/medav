// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
)

// Config holds all runtime settings. The service is single-user and performs no
// authentication of its own — a reverse proxy / ingress controller is expected
// to authenticate requests upstream.
type Config struct {
	// DatabaseURL is a libpq/pgx connection string, e.g.
	// postgres://user:pass@host:5432/medav?sslmode=disable
	DatabaseURL string
	// ListenAddr is the TCP address the HTTP server binds to. Plain HTTP only;
	// TLS is terminated by the proxy. Defaults to ":8080".
	ListenAddr string
	// Prefix is the URL path prefix the CalDAV handler is mounted under. Empty
	// means mounted at the root. It must not end with a trailing slash.
	Prefix string
}

// Load reads configuration from environment variables, applying defaults.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		ListenAddr:  getenv("LISTEN_ADDR", ":8080"),
		Prefix:      os.Getenv("PREFIX"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
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
