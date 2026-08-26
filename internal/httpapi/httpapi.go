// Package httpapi routes everything this service exposes.
//
// Three groups:
//
//	the pairing API      talked to by the PocketClaw app
//	the Telegram webhook talked to by Telegram, authenticated by a shared secret
//	the setup page       talked to by the operator, in a browser
//
// The manager bot token never crosses any of these boundaries. The setup page
// asks the server to act; it is never handed a credential to act with.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/config"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/onboarding"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/pairing"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/telegram"
)

// WebhookManager is the slice of the Telegram client the setup page needs.
type WebhookManager interface {
	SetWebhook(ctx context.Context, webhookURL, secret string) error
	GetWebhookInfo(ctx context.Context) (telegram.WebhookInfo, error)
}

// Server holds everything the routes need.
type Server struct {
	cfg      config.Config
	problems []string
	service  *onboarding.Service
	store    pairing.Store
	webhooks WebhookManager
	log      *slog.Logger
}

// New returns a Server. problems is the list of configuration errors from
// config.Load; the setup page renders them so an operator can see what is
// missing rather than meeting an opaque failure.
func New(cfg config.Config, problems []string, service *onboarding.Service, store pairing.Store, webhooks WebhookManager, log *slog.Logger) *Server {
	return &Server{cfg: cfg, problems: problems, service: service, store: store, webhooks: webhooks, log: log}
}

// Handler returns the routed HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Pairing API — the PocketClaw app.
	mux.HandleFunc("POST /telegram/pairings", s.createPairing)
	mux.HandleFunc("GET /telegram/pairings/{id}", s.getPairing)
	mux.HandleFunc("POST /telegram/pairings/{id}/token", s.collectToken)

	// Telegram.
	mux.HandleFunc("POST /telegram/webhook", s.webhook)

	// Operator.
	mux.HandleFunc("GET /", s.setupPage)
	mux.HandleFunc("GET /privacy", s.privacyPage)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/verify-telegram", s.verifyTelegram)
	mux.HandleFunc("POST /api/register-webhook", s.registerWebhook)
	mux.HandleFunc("POST /api/check-storage", s.checkStorage)
	mux.HandleFunc("POST /api/test-pairing", s.testPairing)

	return mux
}

// ---------------------------------------------------------------- pairing API

type createResponse struct {
	PairingID           string `json:"pairing_id"`
	PollToken           string `json:"poll_token"`
	SuggestedUsername   string `json:"suggested_username"`
	SuggestedName       string `json:"suggested_name"`
	DeepLink            string `json:"deep_link"`
	QRPayload           string `json:"qr_payload"`
	ExpiresAt           string `json:"expires_at"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

type statusResponse struct {
	PairingID   string `json:"pairing_id"`
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	OwnerUserID int64  `json:"owner_user_id,omitempty"`
	ExpiresAt   string `json:"expires_at"`
}

type tokenResponse struct {
	BotToken    string `json:"bot_token"`
	BotUserID   int64  `json:"bot_user_id"`
	BotUsername string `json:"bot_username"`
	OwnerUserID int64  `json:"owner_user_id"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (s *Server) createPairing(w http.ResponseWriter, r *http.Request) {
	rec, pollToken, err := s.service.CreatePairing(r.Context())
	if errors.Is(err, pairing.ErrStorageNotConfigured) {
		// Distinct from a generic outage: this deployment is incomplete, and
		// the operator needs to know that rather than see a retryable error.
		s.log.Error("refusing to create a pairing: no storage is connected")
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "storage_not_configured"})
		return
	}
	if err != nil {
		s.log.Error("could not create a pairing", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "pairing_unavailable"})
		return
	}
	// deep_link and qr_payload are deliberately the same string. Telegram
	// defines no distinct QR form, and the QR must carry no secret.
	writeJSON(w, http.StatusCreated, createResponse{
		PairingID:           rec.ID,
		PollToken:           pollToken,
		SuggestedUsername:   rec.SuggestedUsername,
		SuggestedName:       rec.SuggestedName,
		DeepLink:            rec.DeepLink,
		QRPayload:           rec.DeepLink,
		ExpiresAt:           rec.ExpiresAt.UTC().Format(time.RFC3339),
		PollIntervalSeconds: 2,
	})
}

func (s *Server) getPairing(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing_poll_token"})
		return
	}
	rec, err := s.service.Authenticate(r.Context(), r.PathValue("id"), token)
	if err != nil {
		s.writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{
		PairingID:   rec.ID,
		State:       string(rec.State),
		Reason:      rec.Reason,
		BotUsername: rec.BotUsername,
		OwnerUserID: rec.OwnerUserID,
		ExpiresAt:   rec.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) collectToken(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing_poll_token"})
		return
	}
	rec, botToken, err := s.service.CollectToken(r.Context(), r.PathValue("id"), token)
	if err != nil {
		s.writePairingError(w, err)
		return
	}
	s.log.Info("delivered a child bot token", "pairing_id", rec.ID, "bot_username", rec.BotUsername)
	// The token appears here and nowhere else: not in a log line, not in a
	// poll response, not in the deep link.
	writeJSON(w, http.StatusOK, tokenResponse{
		BotToken:    botToken,
		BotUserID:   rec.BotUserID,
		BotUsername: rec.BotUsername,
		OwnerUserID: rec.OwnerUserID,
	})
}

// writePairingError maps errors to status codes without revealing whether a
// pairing exists to a caller holding the wrong token: unknown, expired, and
// unauthorized all answer 404.
func (s *Server) writePairingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pairing.ErrNotFound), errors.Is(err, pairing.ErrUnauthorized):
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "pairing_not_found"})
	case errors.Is(err, pairing.ErrNotReady):
		writeJSON(w, http.StatusConflict, errorResponse{Error: "not_ready"})
	default:
		s.log.Error("unexpected pairing error", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal_error"})
	}
}

// -------------------------------------------------------------------- webhook

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	// Telegram echoes the secret set with setWebhook. Anything else is forged.
	presented := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if s.cfg.WebhookSecret == "" ||
		subtle.ConstantTimeCompare([]byte(presented), []byte(s.cfg.WebhookSecret)) != 1 {
		s.log.Warn("rejected a webhook request with an invalid secret")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var update telegram.Update
	if err := json.Unmarshal(body, &update); err != nil {
		s.log.Warn("could not decode a webhook update")
		// Answer 200 anyway: a non-200 makes Telegram retry an update this
		// service will never be able to parse.
		w.WriteHeader(http.StatusOK)
		return
	}

	s.service.HandleUpdate(r.Context(), update)
	w.WriteHeader(http.StatusOK)
}

// ------------------------------------------------------------------- operator

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"manager_username": s.cfg.ManagerBotUsername,
		"storage":          s.store.Describe(),
	})
}

type checkResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"manager_username": s.cfg.ManagerBotUsername,
		"service_url":      s.cfg.ResolveBaseURL(r),
		"webhook_url":      s.webhookURL(r),
		"storage":          s.store.Describe(),
		"storage_shared":   s.cfg.StorageConfigured(),
		"pairing_ttl":      s.cfg.PairingTTL.String(),
		"problems":         s.problems,
	})
}

func (s *Server) verifyTelegram(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ManagerBotToken == "" {
		writeJSON(w, http.StatusOK, checkResult{Message: "TELEGRAM_MANAGER_BOT_TOKEN is not set"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	me, err := s.service.VerifyManager(ctx)
	if err != nil {
		// VerifyManager distinguishes "wrong bot" from "management disabled",
		// and the telegram client has already redacted the token from any
		// transport error.
		writeJSON(w, http.StatusOK, checkResult{Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, checkResult{
		OK:      true,
		Message: "Connected as @" + me.Username,
		Detail:  "can_manage_bots = true",
	})
}

func (s *Server) registerWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ManagerBotToken == "" || s.cfg.WebhookSecret == "" {
		writeJSON(w, http.StatusOK, checkResult{
			Message: "Set TELEGRAM_MANAGER_BOT_TOKEN and TELEGRAM_WEBHOOK_SECRET first",
		})
		return
	}
	target := s.webhookURL(r)
	if target == "" {
		writeJSON(w, http.StatusOK, checkResult{
			Message: "Could not determine this deployment's public URL; set PUBLIC_BASE_URL",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// The browser asks; the server acts. The token is read from the server
	// environment and never leaves it.
	if err := s.webhooks.SetWebhook(ctx, target, s.cfg.WebhookSecret); err != nil {
		writeJSON(w, http.StatusOK, checkResult{Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, checkResult{OK: true, Message: "Webhook registered", Detail: target})
}

func (s *Server) checkStorage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if errors.Is(s.store.Ping(ctx), pairing.ErrStorageNotConfigured) {
		writeJSON(w, http.StatusOK, checkResult{
			Message: "NOT CONFIGURED",
			Detail: "Connect a Redis database: Vercel project → Storage → Marketplace → " +
				"Upstash for Redis → connect it to this project, then Redeploy.",
		})
		return
	}
	if err := s.store.Ping(ctx); err != nil {
		if errors.Is(err, pairing.ErrStorageNotWritable) {
			// By far the likeliest cause, because the two token names differ
			// by one word and sit next to each other in the dashboard.
			writeJSON(w, http.StatusOK, checkResult{
				Message: "Reachable, but writes are refused",
				Detail: "This usually means a read-only credential. Vercel's Redis integration " +
					"injects both KV_REST_API_TOKEN and KV_REST_API_READ_ONLY_TOKEN — make sure " +
					"the read-write one is in use, then Redeploy.",
			})
			return
		}
		writeJSON(w, http.StatusOK, checkResult{Message: err.Error(), Detail: s.store.Describe()})
		return
	}
	if !s.cfg.StorageConfigured() {
		writeJSON(w, http.StatusOK, checkResult{
			Message: "In-memory (development only)",
			Detail:  "This cannot work across serverless instances. Connect a Redis database before using this deployment.",
		})
		return
	}
	writeJSON(w, http.StatusOK, checkResult{OK: true, Message: "Storage reachable and writable", Detail: s.store.Describe()})
}

// testPairing exercises the real create path end to end, then deletes the
// record. It proves storage writes and reads work without waiting for a phone.
func (s *Server) testPairing(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rec, _, err := s.service.CreatePairing(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, checkResult{Message: err.Error()})
		return
	}
	readBack, err := s.store.Get(ctx, rec.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, checkResult{Message: "wrote a pairing but could not read it back: " + err.Error()})
		return
	}
	if err := s.store.Delete(ctx, rec.ID); err != nil {
		s.log.Warn("could not clean up a test pairing", "pairing_id", rec.ID, "error", err)
	}
	writeJSON(w, http.StatusOK, checkResult{
		OK:      true,
		Message: "Created and read back a pairing",
		// The deep link is public, so showing it is safe and it is the most
		// useful thing an operator can see here.
		Detail: readBack.DeepLink,
	})
}

func (s *Server) webhookURL(r *http.Request) string {
	base := s.cfg.ResolveBaseURL(r)
	if base == "" {
		return ""
	}
	return base + "/telegram/webhook"
}

// ------------------------------------------------------------------- plumbing

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	value, ok := strings.CutPrefix(header, "Bearer ")
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
