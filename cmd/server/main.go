// Command server is the whole service.
//
// Vercel's Go framework preset detects a root go.mod plus one of main.go,
// cmd/api/main.go, or cmd/server/main.go, builds it, and runs it listening on
// PORT. The same binary runs locally, so there is no separate production
// entrypoint that could drift from the one that gets tested.
//
// Deliberately not an api/*.go serverless function: a repository that offers
// both an api/ function and a detectable server entrypoint satisfies two
// Vercel build modes at once, the framework preset wins, and any `functions`
// config pointing into api/ then matches nothing and fails the build.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/app"
)

func main() {
	handler, cfg, problems := app.Build()

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "Configuration problems (the setup page lists these too):")
		for _, problem := range problems {
			fmt.Fprintln(os.Stderr, "  - "+problem)
		}
	}
	fmt.Printf("PocketClaw Telegram Setup listening on http://localhost:%s\n", port)
	fmt.Printf("  manager: @%s   pairing TTL: %s\n", cfg.ManagerBotUsername, cfg.PairingTTL)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}
