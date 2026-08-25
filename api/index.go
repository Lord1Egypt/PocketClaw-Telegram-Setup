// Package handler is the Vercel entrypoint.
//
// vercel.json rewrites every path here, so this one function serves the
// pairing API, the Telegram webhook, and the operator pages. Routing happens
// inside internal/httpapi.
package handler

import (
	"net/http"

	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/app"
)

// Handler is the exported http.HandlerFunc Vercel's Go runtime looks for.
func Handler(w http.ResponseWriter, r *http.Request) {
	app.Handler().ServeHTTP(w, r)
}
