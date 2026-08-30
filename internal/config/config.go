// Package config loads server configuration from environment variables.
package config

import (
	"log"
	"os"
	"strings"
	"time"
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
	// AdminEmails is the explicit allowlist for instance-wide update mutations.
	// Empty means nobody can change policy or apply an update.
	AdminEmails []string
	// GoogleClientID and GoogleClientSecret configure Google OIDC sign-in.
	GoogleClientID     string
	GoogleClientSecret string
	// BaseURL is the externally visible URL of the instance, used for OAuth redirects.
	BaseURL string
	// TrustProxy allows the immediate reverse proxy to supply the external scheme
	// and host through X-Forwarded-Proto and X-Forwarded-Host. Leave disabled when
	// WorkTime is reachable directly.
	TrustProxy bool
	// AgentGrace is how long an agent session may go without heartbeats before the
	// reconciliation job closes it at the last heartbeat.
	AgentGrace time.Duration
	// AgentIdle is the largest gap between agent heartbeats still billed as
	// continuous work; a larger gap starts a new time entry.
	AgentIdle time.Duration
	// AgentToolMax caps how much of a gap that began with a tool call is still
	// billed, so a hung tool cannot bill forever.
	AgentToolMax time.Duration
	// AgentReconcile is how often the reconciliation job runs. Configurable so the
	// end-to-end suite can watch a session go stale inside a test timeout.
	AgentReconcile time.Duration
	// UpdateChecks disables every GitHub and Sigstore TUF request when false.
	UpdateChecks bool
}

// Defaults returns the agent timings a deployment gets when nothing is configured.
// They are also the fallback for a Config assembled by hand, so a zero duration can
// never reach the agent policy as a literal zero.
func Defaults() Config {
	return Config{
		AgentGrace:     10 * time.Minute,
		AgentIdle:      10 * time.Minute,
		AgentToolMax:   30 * time.Minute,
		AgentReconcile: time.Minute,
		UpdateChecks:   true,
	}
}

func Load() Config {
	defaults := Defaults()
	cfg := Config{
		Addr:               envOr("WORKTIME_ADDR", ":8080"),
		DBPath:             envOr("WORKTIME_DB", "worktime.db"),
		DevAuth:            os.Getenv("WORKTIME_DEV_AUTH") == "1",
		GoogleClientID:     os.Getenv("WORKTIME_GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("WORKTIME_GOOGLE_CLIENT_SECRET"),
		BaseURL:            envOr("WORKTIME_BASE_URL", "http://localhost:8080"),
		TrustProxy:         os.Getenv("WORKTIME_TRUST_PROXY") == "1",
		AgentGrace:         envDuration("WORKTIME_AGENT_GRACE", defaults.AgentGrace),
		AgentIdle:          envDuration("WORKTIME_AGENT_IDLE", defaults.AgentIdle),
		AgentToolMax:       envDuration("WORKTIME_AGENT_TOOL_MAX", defaults.AgentToolMax),
		AgentReconcile:     envDuration("WORKTIME_AGENT_RECONCILE", defaults.AgentReconcile),
		UpdateChecks:       os.Getenv("WORKTIME_UPDATE_CHECKS") != "0",
	}
	for _, email := range strings.Split(os.Getenv("WORKTIME_ALLOWED_EMAILS"), ",") {
		if email = strings.TrimSpace(email); email != "" {
			cfg.AllowedEmails = append(cfg.AllowedEmails, strings.ToLower(email))
		}
	}
	for _, email := range strings.Split(os.Getenv("WORKTIME_ADMIN_EMAILS"), ",") {
		if email = strings.TrimSpace(email); email != "" {
			cfg.AdminEmails = append(cfg.AdminEmails, strings.ToLower(email))
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

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		log.Printf("config: ignoring %s=%q, using %s", key, value, fallback)
		return fallback
	}
	return parsed
}
