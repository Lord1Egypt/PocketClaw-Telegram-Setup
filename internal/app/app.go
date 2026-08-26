// Package app wires the service together from the environment.
//
// cmd/server uses it, and that is the only entrypoint: the same binary serves
// locally and on Vercel, so there is nothing for a second entrypoint to drift
// from.
package app

import (
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/config"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/httpapi"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/onboarding"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/pairing"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/telegram"
)

// Build assembles the HTTP handler from the current environment.
//
// It never fails on a missing secret. A misconfigured deployment still serves
// the setup page, which lists exactly what is missing — that page is the only
// tool an operator has before the service works.
func Build() (http.Handler, config.Config, []string) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, problems := config.Load()

	var store pairing.Store
	switch {
	case cfg.StorageConfigured():
		store = pairing.NewRedisStore(cfg.RedisURL, cfg.RedisToken, nil)
	case cfg.AllowMemoryStore:
		// Local development only, and opted into explicitly.
		store = pairing.NewMemoryStore()
	default:
		// Fail closed. A silent in-memory fallback would make a storage-less
		// deployment look healthy right up to the point where a pairing never
		// completes; see pairing.UnconfiguredStore.
		store = pairing.NewUnconfiguredStore()
	}

	// A missing PAIRING_SECRET is already reported as a problem. Use a
	// per-process random key so the service still runs coherently for the
	// setup page rather than panicking on a nil hasher.
	hasher, err := pairing.NewHasher(cfg.PairingSecret)
	if err != nil {
		fallback, randErr := pairing.NewPollToken()
		if randErr != nil {
			panic("cannot generate a fallback pairing key: " + randErr.Error())
		}
		hasher, _ = pairing.NewHasher(fallback)
	}

	bot := telegram.New(cfg.ManagerBotToken, nil)
	service := onboarding.New(store, hasher, bot, cfg.ManagerBotUsername, cfg.PairingTTL, log)
	server := httpapi.New(cfg, problems, service, store, bot, log)

	return server.Handler(), cfg, problems
}

var (
	once    sync.Once
	handler http.Handler
)

// Handler returns a process-wide handler, built on first use.
//
// A serverless instance handles many requests; rebuilding the stack for each
// one would re-read the environment and re-allocate clients for no reason.
func Handler() http.Handler {
	once.Do(func() { handler, _, _ = Build() })
	return handler
}
