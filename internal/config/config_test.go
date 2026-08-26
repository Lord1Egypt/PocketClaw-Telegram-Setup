package config

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func setEnv(t *testing.T, pairs map[string]string) {
	t.Helper()
	for key, value := range pairs {
		t.Setenv(key, value)
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"TELEGRAM_MANAGER_BOT_TOKEN":    "123456:MANAGER-TOKEN",
		"TELEGRAM_MANAGER_BOT_USERNAME": "PocketClawSetupBot",
		"TELEGRAM_WEBHOOK_SECRET":       "webhook-secret-long-enough",
		"PAIRING_SECRET":                "pairing-secret-long-enough",
		"UPSTASH_REDIS_REST_URL":        "https://storage.example.upstash.io",
		"UPSTASH_REDIS_REST_TOKEN":      "storage-token",
	}
}

func TestLoadAcceptsACompleteEnvironment(t *testing.T) {
	setEnv(t, validEnv())
	cfg, problems := Load()
	if len(problems) != 0 {
		t.Fatalf("a complete environment reported problems: %v", problems)
	}
	if !cfg.StorageConfigured() {
		t.Fatal("storage was not detected")
	}
	if cfg.ManagerBotUsername != "PocketClawSetupBot" {
		t.Fatalf("username = %q", cfg.ManagerBotUsername)
	}
	if cfg.PairingTTL.Minutes() != 10 {
		t.Fatalf("default TTL = %s, want 10m", cfg.PairingTTL)
	}
}

func TestLoadStripsTheAtPrefixFromTheUsername(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_MANAGER_BOT_USERNAME"] = "@PocketClawSetupBot"
	setEnv(t, env)
	cfg, _ := Load()
	if cfg.ManagerBotUsername != "PocketClawSetupBot" {
		t.Fatalf("username = %q", cfg.ManagerBotUsername)
	}
}

// A project migrated from the retired Vercel KV still carries the old names.
func TestLoadAcceptsLegacyVercelKVNames(t *testing.T) {
	env := validEnv()
	delete(env, "UPSTASH_REDIS_REST_URL")
	delete(env, "UPSTASH_REDIS_REST_TOKEN")
	env["UPSTASH_REDIS_REST_URL"] = ""
	env["UPSTASH_REDIS_REST_TOKEN"] = ""
	env["KV_REST_API_URL"] = "https://storage.example.upstash.io"
	env["KV_REST_API_TOKEN"] = "storage-token"
	setEnv(t, env)
	cfg, problems := Load()
	if !cfg.StorageConfigured() {
		t.Fatalf("legacy Vercel KV names were not recognised: %v", problems)
	}
}

func TestLoadAcceptsGenericRedisRestNames(t *testing.T) {
	env := validEnv()
	env["UPSTASH_REDIS_REST_URL"] = ""
	env["UPSTASH_REDIS_REST_TOKEN"] = ""
	env["REDIS_REST_URL"] = "https://redis.example.org"
	env["REDIS_REST_TOKEN"] = "storage-token"
	setEnv(t, env)
	cfg, problems := Load()
	if !cfg.StorageConfigured() {
		t.Fatalf("generic REDIS_REST_* names were not recognised: %v", problems)
	}
	if cfg.RedisURL != "https://redis.example.org" {
		t.Fatalf("RedisURL = %q", cfg.RedisURL)
	}
}

func TestUpstashNamesWinOverLegacyNames(t *testing.T) {
	env := validEnv()
	env["KV_REST_API_URL"] = "https://legacy.example.org"
	env["KV_REST_API_TOKEN"] = "legacy-token"
	setEnv(t, env)
	cfg, _ := Load()
	if cfg.RedisURL != "https://storage.example.upstash.io" {
		t.Fatalf("the legacy name won over the current one: %q", cfg.RedisURL)
	}
}

func TestLoadReportsEveryMissingSecret(t *testing.T) {
	// A bare environment must explain all of it at once, not one item at a time.
	setEnv(t, map[string]string{
		"TELEGRAM_MANAGER_BOT_TOKEN":    "",
		"TELEGRAM_MANAGER_BOT_USERNAME": "",
		"TELEGRAM_WEBHOOK_SECRET":       "",
		"PAIRING_SECRET":                "",
		"UPSTASH_REDIS_REST_URL":        "",
		"UPSTASH_REDIS_REST_TOKEN":      "",
		"KV_REST_API_URL":               "",
		"KV_REST_API_TOKEN":             "",
		"REDIS_REST_URL":                "",
		"REDIS_REST_TOKEN":              "",
	})
	_, problems := Load()
	joined := strings.Join(problems, "\n")
	for _, expected := range []string{
		"TELEGRAM_MANAGER_BOT_TOKEN",
		"TELEGRAM_MANAGER_BOT_USERNAME",
		"TELEGRAM_WEBHOOK_SECRET",
		"PAIRING_SECRET",
		"Pairing storage is NOT CONFIGURED",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("problems do not mention %q:\n%s", expected, joined)
		}
	}
}

func TestLoadRejectsShortSecrets(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_WEBHOOK_SECRET"] = "short"
	env["PAIRING_SECRET"] = "short"
	setEnv(t, env)
	_, problems := Load()
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "TELEGRAM_WEBHOOK_SECRET") || !strings.Contains(joined, "PAIRING_SECRET") {
		t.Fatalf("short secrets were accepted: %v", problems)
	}
}

func TestLoadWarnsLoudlyAboutTheMemoryStore(t *testing.T) {
	env := validEnv()
	env["UPSTASH_REDIS_REST_URL"] = ""
	env["UPSTASH_REDIS_REST_TOKEN"] = ""
	env["ALLOW_MEMORY_STORE"] = "true"
	setEnv(t, env)
	_, problems := Load()
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "DEVELOPMENT ONLY") {
		t.Fatalf("the in-memory fallback is not flagged as development only: %v", problems)
	}
}

func TestResolveBaseURLPrefersExplicitConfiguration(t *testing.T) {
	t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "prod.example.app")
	t.Setenv("VERCEL_URL", "deployment.example.app")
	cfg := Config{PublicBaseURL: "https://custom.example.org"}
	if got := cfg.ResolveBaseURL(httptest.NewRequest(http.MethodGet, "/", nil)); got != "https://custom.example.org" {
		t.Fatalf("ResolveBaseURL = %q", got)
	}
}

func TestResolveBaseURLPrefersTheProductionDomain(t *testing.T) {
	t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "prod.example.app")
	t.Setenv("VERCEL_URL", "deployment.example.app")
	cfg := Config{}
	// The per-deployment URL changes on every deploy; a webhook registered
	// against it would break on the next one.
	if got := cfg.ResolveBaseURL(httptest.NewRequest(http.MethodGet, "/", nil)); got != "https://prod.example.app" {
		t.Fatalf("ResolveBaseURL = %q", got)
	}
}

func TestResolveBaseURLFallsBackToVercelURL(t *testing.T) {
	t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "")
	t.Setenv("VERCEL_URL", "deployment.example.app")
	cfg := Config{}
	if got := cfg.ResolveBaseURL(httptest.NewRequest(http.MethodGet, "/", nil)); got != "https://deployment.example.app" {
		t.Fatalf("ResolveBaseURL = %q", got)
	}
}

func TestResolveBaseURLFallsBackToTheRequestHost(t *testing.T) {
	t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "")
	t.Setenv("VERCEL_URL", "")
	cfg := Config{}

	proxied := httptest.NewRequest(http.MethodGet, "/", nil)
	proxied.Host = "setup.example.org"
	proxied.Header.Set("X-Forwarded-Proto", "https")
	if got := cfg.ResolveBaseURL(proxied); got != "https://setup.example.org" {
		t.Fatalf("proxied ResolveBaseURL = %q", got)
	}

	local := httptest.NewRequest(http.MethodGet, "/", nil)
	local.Host = "localhost:3000"
	if got := cfg.ResolveBaseURL(local); got != "http://localhost:3000" {
		t.Fatalf("local ResolveBaseURL = %q", got)
	}
}
