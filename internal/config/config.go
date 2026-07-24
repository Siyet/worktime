// Package config loads server configuration from environment variables.
package config

import (
	"os"
	"strings"
)

type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// DBPath is the SQLite database file path.
	DBPath string
	// DevAuth enables automatic authentication as a local dev user. Never enable in production.
	DevAuth bool
	// AllowedEmails restricts Google sign-in to the listed addresses. Empty list allows everyone.
	AllowedEmails []string
	// GoogleClientID and GoogleClientSecret configure Google OIDC sign-in.
	GoogleClientID     string
	GoogleClientSecret string
	// BaseURL is the externally visible URL of the instance, used for OAuth redirects.
	BaseURL string
}

func Load() Config {
	cfg := Config{
		Addr:               envOr("WORKTIME_ADDR", ":8080"),
		DBPath:             envOr("WORKTIME_DB", "worktime.db"),
		DevAuth:            os.Getenv("WORKTIME_DEV_AUTH") == "1",
		GoogleClientID:     os.Getenv("WORKTIME_GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("WORKTIME_GOOGLE_CLIENT_SECRET"),
		BaseURL:            envOr("WORKTIME_BASE_URL", "http://localhost:8080"),
	}
	for _, email := range strings.Split(os.Getenv("WORKTIME_ALLOWED_EMAILS"), ",") {
		if email = strings.TrimSpace(email); email != "" {
			cfg.AllowedEmails = append(cfg.AllowedEmails, strings.ToLower(email))
		}
	}
	return cfg
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
