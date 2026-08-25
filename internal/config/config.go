// Package config loads the service's environment.
//
// Every secret here is server-side. Nothing in this struct is ever rendered
// into a page, returned from an API, or written to a log.
package config

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/naming"
)

// Config is the resolved service configuration.
type Config struct {
	// ManagerBotToken is a SERVER SECRET. Never expose it.
	ManagerBotToken string
	// ManagerBotUsername is public; it appears in every deep link.
	ManagerBotUsername string

	// WebhookSecret is echoed by Telegram on every delivery and is what lets
	// the webhook reject forged requests.
	WebhookSecret string

	// PairingSecret keys the stored poll-token verifier.
	PairingSecret string

	// RedisURL and RedisToken address the shared pairing storage.
	RedisURL   string
	RedisToken string

	// AllowMemoryStore permits the process-local store. It is only ever
	// correct for local development; see Load.
	AllowMemoryStore bool

	PairingTTL time.Duration

	// PublicBaseURL is the deployment's own https origin, used to build the
	// Telegram webhook URL.
	PublicBaseURL string
}

// Load reads the environment. It returns the config plus a list of
// human-readable problems; a config with problems is still returned so the
// setup page can explain what is missing instead of failing to start.
func Load() (Config, []string) {
	cfg := Config{
		ManagerBotToken:    env("TELEGRAM_MANAGER_BOT_TOKEN"),
		ManagerBotUsername: strings.TrimPrefix(env("TELEGRAM_MANAGER_BOT_USERNAME"), "@"),
		WebhookSecret:      env("TELEGRAM_WEBHOOK_SECRET"),
		PairingSecret:      env("PAIRING_SECRET"),
		PairingTTL:         10 * time.Minute,
	}

	// Accept both the Vercel KV names and the Upstash names, so an integration
	// added through either route works without renaming variables by hand.
	cfg.RedisURL = firstEnv("KV_REST_API_URL", "UPSTASH_REDIS_REST_URL", "REDIS_REST_URL")
	cfg.RedisToken = firstEnv("KV_REST_API_TOKEN", "UPSTASH_REDIS_REST_TOKEN", "REDIS_REST_TOKEN")

	cfg.AllowMemoryStore = env("ALLOW_MEMORY_STORE") == "true"
	cfg.PublicBaseURL = strings.TrimSuffix(env("PUBLIC_BASE_URL"), "/")

	if raw := env("PAIRING_TTL_SECONDS"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			cfg.PairingTTL = time.Duration(seconds) * time.Second
		}
	}

	var problems []string
	if cfg.ManagerBotToken == "" {
		problems = append(problems, "TELEGRAM_MANAGER_BOT_TOKEN is not set")
	}
	if cfg.ManagerBotUsername == "" {
		problems = append(problems, "TELEGRAM_MANAGER_BOT_USERNAME is not set")
	} else if err := naming.ValidateUsername(cfg.ManagerBotUsername); err != nil {
		problems = append(problems, "TELEGRAM_MANAGER_BOT_USERNAME is invalid: "+err.Error())
	}
	if len(cfg.WebhookSecret) < 16 {
		problems = append(problems, "TELEGRAM_WEBHOOK_SECRET is missing or shorter than 16 characters")
	}
	if len(cfg.PairingSecret) < 16 {
		problems = append(problems, "PAIRING_SECRET is missing or shorter than 16 characters")
	}
	if cfg.RedisURL == "" || cfg.RedisToken == "" {
		if cfg.AllowMemoryStore {
			problems = append(problems,
				"no shared storage configured; using the in-memory store, which is DEVELOPMENT ONLY "+
					"and cannot work across serverless instances")
		} else {
			problems = append(problems,
				"shared storage is not configured: set KV_REST_API_URL and KV_REST_API_TOKEN "+
					"(or the UPSTASH_REDIS_REST_* equivalents)")
		}
	}

	return cfg, problems
}

// StorageConfigured reports whether shared storage credentials are present.
func (c Config) StorageConfigured() bool { return c.RedisURL != "" && c.RedisToken != "" }

// ResolveBaseURL determines this deployment's public https origin.
//
// Vercel exposes the stable production domain and the per-deployment domain
// through different variables, and neither is set when running locally, so the
// request host is the last resort. PUBLIC_BASE_URL overrides everything for
// deployments behind a custom domain or proxy.
func (c Config) ResolveBaseURL(r *http.Request) string {
	if c.PublicBaseURL != "" {
		return c.PublicBaseURL
	}
	if host := env("VERCEL_PROJECT_PRODUCTION_URL"); host != "" {
		return "https://" + strings.TrimSuffix(host, "/")
	}
	if host := env("VERCEL_URL"); host != "" {
		return "https://" + strings.TrimSuffix(host, "/")
	}
	if r == nil {
		return ""
	}
	scheme := "https"
	// A local dev server has no TLS. Anything else is assumed to be behind a
	// TLS-terminating proxy, which is how Vercel serves this.
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = forwarded
	} else if r.TLS == nil && isLocalHost(r.Host) {
		scheme = "http"
	}
	if r.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}

func isLocalHost(host string) bool {
	return strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1")
}

func env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := env(key); value != "" {
			return value
		}
	}
	return ""
}
