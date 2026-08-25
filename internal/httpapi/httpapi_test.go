package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/config"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/onboarding"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/pairing"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/telegram"
)

const (
	testWebhookSecret = "webhook-secret-long-enough"
	testChildToken    = "9001:CHILD-TOKEN"
)

type fakeBot struct {
	canManageBots bool
	username      string
	tokenCalls    int
	sent          []string
	webhookURL    string
	webhookSecret string
	setWebhookErr error
}

func (f *fakeBot) GetMe(context.Context) (telegram.User, error) {
	return telegram.User{ID: 1, IsBot: true, Username: f.username, CanManageBots: f.canManageBots}, nil
}
func (f *fakeBot) GetManagedBotToken(_ context.Context, _ int64) (string, error) {
	f.tokenCalls++
	return testChildToken, nil
}
func (f *fakeBot) SendMessage(_ context.Context, _ int64, text string) error {
	f.sent = append(f.sent, text)
	return nil
}
func (f *fakeBot) SetWebhook(_ context.Context, webhookURL, secret string) error {
	if f.setWebhookErr != nil {
		return f.setWebhookErr
	}
	f.webhookURL, f.webhookSecret = webhookURL, secret
	return nil
}
func (f *fakeBot) GetWebhookInfo(context.Context) (telegram.WebhookInfo, error) {
	return telegram.WebhookInfo{URL: f.webhookURL}, nil
}

type harness struct {
	server *httptest.Server
	bot    *fakeBot
	store  *pairing.MemoryStore
	svc    *onboarding.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	bot := &fakeBot{canManageBots: true, username: "PocketClawSetupBot"}
	store := pairing.NewMemoryStore()
	hasher, err := pairing.NewHasher("pairing-secret-long-enough")
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		ManagerBotToken:    "123456:MANAGER-TOKEN",
		ManagerBotUsername: "PocketClawSetupBot",
		WebhookSecret:      testWebhookSecret,
		PairingSecret:      "pairing-secret-long-enough",
		PairingTTL:         10 * time.Minute,
		RedisURL:           "https://storage.invalid",
		RedisToken:         "storage-token",
	}
	svc := onboarding.New(store, hasher, bot, "PocketClawSetupBot", cfg.PairingTTL, log)
	api := New(cfg, nil, svc, store, bot, log)
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	return &harness{server: server, bot: bot, store: store, svc: svc}
}

func (h *harness) do(t *testing.T, method, path, bearer string, body []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, out
}

type createBody struct {
	PairingID         string `json:"pairing_id"`
	PollToken         string `json:"poll_token"`
	SuggestedUsername string `json:"suggested_username"`
	SuggestedName     string `json:"suggested_name"`
	DeepLink          string `json:"deep_link"`
	QRPayload         string `json:"qr_payload"`
	ExpiresAt         string `json:"expires_at"`
}

func (h *harness) create(t *testing.T) createBody {
	t.Helper()
	resp, body := h.do(t, http.MethodPost, "/telegram/pairings", "", nil, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d: %s", resp.StatusCode, body)
	}
	var out createBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// deliverManagedBot posts a real Telegram-shaped webhook payload.
func (h *harness) deliverManagedBot(t *testing.T, username, secret string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"update_id": 1,
		"managed_bot": map[string]any{
			"user": map[string]any{"id": 555, "first_name": "Ada"},
			"bot":  map[string]any{"id": 9001, "is_bot": true, "username": username},
		},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	resp, _ := h.do(t, http.MethodPost, "/telegram/webhook", "", payload,
		map[string]string{"X-Telegram-Bot-Api-Secret-Token": secret})
	return resp
}

func TestCreatePairingShape(t *testing.T) {
	h := newHarness(t)
	got := h.create(t)

	if len(got.PairingID) != 32 || len(got.PollToken) != 64 {
		t.Fatalf("identifiers have the wrong shape: %+v", got)
	}
	if got.DeepLink != got.QRPayload {
		t.Fatalf("qr_payload differs from deep_link")
	}
	if !strings.HasPrefix(got.DeepLink, "https://t.me/newbot/PocketClawSetupBot/") {
		t.Fatalf("deep_link = %q", got.DeepLink)
	}
	if got.SuggestedName != "PocketClaw Agent" {
		t.Fatalf("suggested_name = %q", got.SuggestedName)
	}
	if !strings.HasPrefix(got.SuggestedUsername, "pocketclaw_") {
		t.Fatalf("suggested_username = %q", got.SuggestedUsername)
	}
	if _, err := time.Parse(time.RFC3339, got.ExpiresAt); err != nil {
		t.Fatalf("expires_at = %q", got.ExpiresAt)
	}
	for _, secret := range []string{got.PollToken, testChildToken} {
		if strings.Contains(got.DeepLink, secret) {
			t.Fatalf("deep_link carries a secret")
		}
	}
}

func TestFullFlowThroughTheWebhook(t *testing.T) {
	h := newHarness(t)
	created := h.create(t)

	if resp := h.deliverManagedBot(t, created.SuggestedUsername, testWebhookSecret); resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook returned %d", resp.StatusCode)
	}

	resp, body := h.do(t, http.MethodGet, "/telegram/pairings/"+created.PairingID, created.PollToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll returned %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), testChildToken) {
		t.Fatalf("the poll response leaked the bot token: %s", body)
	}
	var status struct {
		State       string `json:"state"`
		BotUsername string `json:"bot_username"`
		OwnerUserID int64  `json:"owner_user_id"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.State != "ready" || status.OwnerUserID != 555 || status.BotUsername != created.SuggestedUsername {
		t.Fatalf("status = %+v", status)
	}

	resp, body = h.do(t, http.MethodPost, "/telegram/pairings/"+created.PairingID+"/token", created.PollToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("collect returned %d: %s", resp.StatusCode, body)
	}
	var delivered struct {
		BotToken    string `json:"bot_token"`
		BotUserID   int64  `json:"bot_user_id"`
		OwnerUserID int64  `json:"owner_user_id"`
	}
	if err := json.Unmarshal(body, &delivered); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if delivered.BotToken != testChildToken || delivered.BotUserID != 9001 || delivered.OwnerUserID != 555 {
		t.Fatalf("delivery = %+v", delivered)
	}

	// Single use, and the session is gone.
	resp, _ = h.do(t, http.MethodPost, "/telegram/pairings/"+created.PairingID+"/token", created.PollToken, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("replaying collection returned %d, want 404", resp.StatusCode)
	}
	if h.store.Len() != 0 {
		t.Fatalf("%d records remain after delivery", h.store.Len())
	}
}

func TestWebhookRejectsAMissingOrWrongSecret(t *testing.T) {
	h := newHarness(t)
	created := h.create(t)

	for _, secret := range []string{"", "not-the-secret"} {
		resp := h.deliverManagedBot(t, created.SuggestedUsername, secret)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("webhook with secret %q returned %d, want 403", secret, resp.StatusCode)
		}
	}
	if h.bot.tokenCalls != 0 {
		t.Fatal("a forged webhook triggered a token retrieval")
	}

	// The pairing must be untouched by the rejected attempts.
	_, body := h.do(t, http.MethodGet, "/telegram/pairings/"+created.PairingID, created.PollToken, nil, nil)
	if !strings.Contains(string(body), `"pending"`) {
		t.Fatalf("a forged webhook advanced the pairing: %s", body)
	}
}

func TestWebhookIgnoresAnUnrelatedBot(t *testing.T) {
	h := newHarness(t)
	created := h.create(t)

	if resp := h.deliverManagedBot(t, "somebody_elses_bot", testWebhookSecret); resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook returned %d", resp.StatusCode)
	}
	if h.bot.tokenCalls != 0 {
		t.Fatal("a token was fetched for a bot with no matching pairing")
	}
	_, body := h.do(t, http.MethodGet, "/telegram/pairings/"+created.PairingID, created.PollToken, nil, nil)
	if !strings.Contains(string(body), `"pending"`) {
		t.Fatalf("an unrelated bot advanced the pairing: %s", body)
	}
}

func TestWebhookAnswersStart(t *testing.T) {
	h := newHarness(t)
	payload, _ := json.Marshal(map[string]any{
		"update_id": 2,
		"message":   map[string]any{"message_id": 1, "chat": map[string]any{"id": 42}, "text": "/start"},
	})
	resp, _ := h.do(t, http.MethodPost, "/telegram/webhook", "", payload,
		map[string]string{"X-Telegram-Bot-Api-Secret-Token": testWebhookSecret})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook returned %d", resp.StatusCode)
	}
	if len(h.bot.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.bot.sent))
	}
	reply := h.bot.sent[0]
	if !strings.Contains(reply, "PocketClaw Setup") {
		t.Fatalf("/start reply is not PocketClaw-branded: %q", reply)
	}
	for _, forbidden := range []string{"PicoClaw", "Hermes", "Sipeed", "MANAGER-TOKEN", "webhook"} {
		if strings.Contains(reply, forbidden) {
			t.Fatalf("/start reply mentions %q: %q", forbidden, reply)
		}
	}
}

func TestWebhookTolerateseGarbageWithoutRetryStorm(t *testing.T) {
	h := newHarness(t)
	resp, _ := h.do(t, http.MethodPost, "/telegram/webhook", "", []byte("not json"),
		map[string]string{"X-Telegram-Bot-Api-Secret-Token": testWebhookSecret})
	// 200: a non-200 makes Telegram retry an update that can never be parsed.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook returned %d for undecodable input, want 200", resp.StatusCode)
	}
}

func TestPollingRequiresTheCorrectToken(t *testing.T) {
	h := newHarness(t)
	created := h.create(t)

	resp, _ := h.do(t, http.MethodGet, "/telegram/pairings/"+created.PairingID, "", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("polling without a token returned %d, want 401", resp.StatusCode)
	}
	// A wrong token and an unknown pairing must be indistinguishable.
	resp, _ = h.do(t, http.MethodGet, "/telegram/pairings/"+created.PairingID, strings.Repeat("0", 64), nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("polling with a wrong token returned %d, want 404", resp.StatusCode)
	}
	resp, _ = h.do(t, http.MethodGet, "/telegram/pairings/"+strings.Repeat("f", 32), strings.Repeat("0", 64), nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("polling an unknown pairing returned %d, want 404", resp.StatusCode)
	}
}

func TestCollectionRequiresTheCorrectTokenAndDoesNotBurnDelivery(t *testing.T) {
	h := newHarness(t)
	created := h.create(t)
	h.deliverManagedBot(t, created.SuggestedUsername, testWebhookSecret)

	resp, body := h.do(t, http.MethodPost, "/telegram/pairings/"+created.PairingID+"/token", strings.Repeat("0", 64), nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("collecting with a wrong token returned %d, want 404", resp.StatusCode)
	}
	if strings.Contains(string(body), testChildToken) {
		t.Fatalf("an unauthorized response leaked the token: %s", body)
	}
	resp, _ = h.do(t, http.MethodPost, "/telegram/pairings/"+created.PairingID+"/token", created.PollToken, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the rightful owner got %d after someone else's failed attempt", resp.StatusCode)
	}
}

func TestCollectionBeforeReadyConflicts(t *testing.T) {
	h := newHarness(t)
	created := h.create(t)
	resp, body := h.do(t, http.MethodPost, "/telegram/pairings/"+created.PairingID+"/token", created.PollToken, nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("collecting while pending returned %d, want 409", resp.StatusCode)
	}
	if strings.Contains(string(body), testChildToken) {
		t.Fatalf("response leaked a token: %s", body)
	}
}

func TestSetupPageRendersAndLeaksNothing(t *testing.T) {
	h := newHarness(t)
	resp, body := h.do(t, http.MethodGet, "/", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("setup page returned %d", resp.StatusCode)
	}
	page := string(body)
	if !strings.Contains(page, "PocketClaw Telegram Setup") {
		t.Fatalf("setup page did not render")
	}
	if !strings.Contains(page, "@PocketClawSetupBot") {
		t.Fatalf("setup page does not name the manager bot")
	}
	for _, secret := range []string{"123456:MANAGER-TOKEN", "MANAGER-TOKEN", testWebhookSecret,
		"pairing-secret-long-enough", "storage-token"} {
		if strings.Contains(page, secret) {
			t.Fatalf("the setup page leaks %q", secret)
		}
	}
}

func TestPrivacyPageRenders(t *testing.T) {
	h := newHarness(t)
	resp, body := h.do(t, http.MethodGet, "/privacy", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("privacy page returned %d", resp.StatusCode)
	}
	page := string(body)
	for _, expected := range []string{"Privacy", "does not process", "AI provider API keys", "Retention"} {
		if !strings.Contains(page, expected) {
			t.Fatalf("privacy page is missing %q", expected)
		}
	}
}

func TestVerifyTelegramReportsCanManageBots(t *testing.T) {
	h := newHarness(t)
	resp, body := h.do(t, http.MethodPost, "/api/verify-telegram", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify returned %d", resp.StatusCode)
	}
	var result struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.OK || !strings.Contains(result.Detail, "can_manage_bots = true") {
		t.Fatalf("verify result = %+v", result)
	}
	if strings.Contains(string(body), "MANAGER-TOKEN") {
		t.Fatalf("verify response leaks the manager token: %s", body)
	}
}

func TestVerifyTelegramFailsClosedWithoutManagementMode(t *testing.T) {
	h := newHarness(t)
	h.bot.canManageBots = false

	_, body := h.do(t, http.MethodPost, "/api/verify-telegram", "", nil, nil)
	var result struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.OK {
		t.Fatal("verify reported success for a bot without bot management enabled")
	}
	if !strings.Contains(result.Message, "Bot Management Mode") {
		t.Fatalf("the error does not tell the operator what to enable: %q", result.Message)
	}
}

func TestVerifyTelegramRejectsAUsernameMismatch(t *testing.T) {
	h := newHarness(t)
	h.bot.username = "SomeOtherBot"

	_, body := h.do(t, http.MethodPost, "/api/verify-telegram", "", nil, nil)
	var result struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &result)
	if result.OK {
		t.Fatal("verify accepted a token belonging to a different bot")
	}
	if !strings.Contains(result.Message, "does not match") {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestRegisterWebhookActsServerSide(t *testing.T) {
	h := newHarness(t)
	resp, body := h.do(t, http.MethodPost, "/api/register-webhook", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register returned %d", resp.StatusCode)
	}
	var result struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.OK {
		t.Fatalf("register failed: %s", body)
	}
	if !strings.HasSuffix(h.bot.webhookURL, "/telegram/webhook") {
		t.Fatalf("webhook url = %q", h.bot.webhookURL)
	}
	if h.bot.webhookSecret != testWebhookSecret {
		t.Fatalf("webhook secret was not sent to Telegram")
	}
	// The secret goes to Telegram, never back to the browser.
	if strings.Contains(string(body), testWebhookSecret) {
		t.Fatalf("the register response leaks the webhook secret: %s", body)
	}
	if strings.Contains(string(body), "MANAGER-TOKEN") {
		t.Fatalf("the register response leaks the manager token: %s", body)
	}
}

func TestCheckStorageAndTestPairing(t *testing.T) {
	h := newHarness(t)

	_, body := h.do(t, http.MethodPost, "/api/check-storage", "", nil, nil)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("check-storage = %s", body)
	}

	_, body = h.do(t, http.MethodPost, "/api/test-pairing", "", nil, nil)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("test-pairing = %s", body)
	}
	if strings.Contains(string(body), "poll_token") {
		t.Fatalf("test-pairing leaked a poll token: %s", body)
	}
	// A test pairing must not linger and hold its username.
	if h.store.Len() != 0 {
		t.Fatalf("%d test pairings were left behind", h.store.Len())
	}
}

func TestStatusCarriesNoSecrets(t *testing.T) {
	h := newHarness(t)
	_, body := h.do(t, http.MethodGet, "/api/status", "", nil, nil)
	for _, secret := range []string{"123456:MANAGER-TOKEN", testWebhookSecret,
		"pairing-secret-long-enough", "storage-token"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("status leaks %q: %s", secret, body)
		}
	}
	if !strings.Contains(string(body), "PocketClawSetupBot") {
		t.Fatalf("status does not report the manager: %s", body)
	}
}

func TestResponsesAreNotCacheable(t *testing.T) {
	h := newHarness(t)
	created := h.create(t)
	resp, _ := h.do(t, http.MethodGet, "/telegram/pairings/"+created.PairingID, created.PollToken, nil, nil)
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	h := newHarness(t)
	resp, _ := h.do(t, http.MethodGet, "/does-not-exist", "", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path returned %d, want 404", resp.StatusCode)
	}
}
