// Command server runs the onboarding service locally.
//
// Production runs on Vercel through api/index.go; this exists so the service
// can be exercised on a laptop with the same wiring.
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
