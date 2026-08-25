package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const fakeToken = "123456:AAHfakemanagertokenvaluenotreal00000"

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := New(fakeToken, server.Client())
	c.SetBaseURL(server.URL)
	return c
}

func TestGetMeParsesCanManageBots(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"username":"PocketClawSetupBot","can_manage_bots":true}}`))
	})
	me, err := c.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if !me.CanManageBots || me.Username != "PocketClawSetupBot" {
		t.Fatalf("GetMe = %+v", me)
	}
}

func TestSetWebhookSendsSecretAndAllowedUpdates(t *testing.T) {
	var got url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.Form
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	})
	if err := c.SetWebhook(context.Background(), "https://example.org/telegram/webhook", "shhh"); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	if got.Get("url") != "https://example.org/telegram/webhook" {
		t.Fatalf("url = %q", got.Get("url"))
	}
	if got.Get("secret_token") != "shhh" {
		t.Fatalf("secret_token = %q", got.Get("secret_token"))
	}
	if got.Get("allowed_updates") != `["managed_bot","message"]` {
		t.Fatalf("allowed_updates = %q", got.Get("allowed_updates"))
	}
	if got.Get("drop_pending_updates") != "true" {
		t.Fatalf("drop_pending_updates = %q", got.Get("drop_pending_updates"))
	}
}

func TestGetWebhookInfo(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"url":"https://example.org/telegram/webhook","pending_update_count":3}}`))
	})
	info, err := c.GetWebhookInfo(context.Background())
	if err != nil {
		t.Fatalf("GetWebhookInfo: %v", err)
	}
	if info.URL != "https://example.org/telegram/webhook" || info.PendingUpdateCount != 3 {
		t.Fatalf("info = %+v", info)
	}
}

func TestUpdateDecodesManagedBot(t *testing.T) {
	var update Update
	raw := `{"update_id":7,"managed_bot":{"user":{"id":555,"first_name":"Ada"},"bot":{"id":9001,"is_bot":true,"username":"pocketclaw_abcd1234_bot"}}}`
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if update.ManagedBot == nil {
		t.Fatal("managed_bot did not decode")
	}
	if update.ManagedBot.User.ID != 555 || update.ManagedBot.Bot.ID != 9001 ||
		update.ManagedBot.Bot.Username != "pocketclaw_abcd1234_bot" {
		t.Fatalf("managed bot update = %+v", update.ManagedBot)
	}
}

func TestUpdateDecodesMessageWithoutManagedBot(t *testing.T) {
	var update Update
	raw := `{"update_id":8,"message":{"message_id":1,"chat":{"id":42},"text":"/start"}}`
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if update.ManagedBot != nil {
		t.Fatal("a plain message decoded as a managed-bot update")
	}
	if update.Message == nil || update.Message.Chat.ID != 42 || update.Message.Text != "/start" {
		t.Fatalf("message = %+v", update.Message)
	}
}

func TestGetManagedBotTokenSendsUserIDAndReturnsToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.FormValue("user_id"); got != "9001" {
			t.Errorf("user_id = %q, want 9001", got)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":"9001:CHILD-TOKEN"}`))
	})
	token, err := c.GetManagedBotToken(context.Background(), 9001)
	if err != nil {
		t.Fatalf("GetManagedBotToken: %v", err)
	}
	if token != "9001:CHILD-TOKEN" {
		t.Fatalf("token = %q", token)
	}
}

func TestGetManagedBotTokenRejectsEmptyResult(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":""}`))
	})
	if _, err := c.GetManagedBotToken(context.Background(), 9001); err == nil {
		t.Fatal("an empty token was accepted")
	}
}

func TestAPIErrorDoesNotEchoTheManagerToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Telegram sometimes quotes the request URL, which contains the token.
		body, _ := json.Marshal(map[string]any{
			"ok":          false,
			"error_code":  401,
			"description": "Unauthorized: bot" + fakeToken + " is invalid",
		})
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(body)
	})
	_, err := c.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("the error echoes the manager token: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("the error was not redacted: %v", err)
	}
}

func TestTransportErrorDoesNotEchoTheManagerToken(t *testing.T) {
	c := New(fakeToken, &http.Client{Timeout: 2 * time.Second})
	// A host that cannot resolve produces an error quoting the full URL.
	c.SetBaseURL("http://pocketclaw-onboarding-invalid.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.GetMe(ctx)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("the transport error echoes the manager token: %v", err)
	}
}

func TestRedact(t *testing.T) {
	secret := "123456:AAsecretpart"
	cases := []struct{ in, want string }{
		{"nothing to hide", "nothing to hide"},
		{"url https://api.telegram.org/bot" + secret + "/getMe", "url https://api.telegram.org/bot[REDACTED]/getMe"},
		// Only the secret half appears, which happens when Telegram echoes it.
		{"leaked AAsecretpart here", "leaked [REDACTED] here"},
	}
	for _, tc := range cases {
		if got := Redact(tc.in, secret); got != tc.want {
			t.Fatalf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := Redact("anything", ""); got != "anything" {
		t.Fatalf("Redact with an empty secret changed the text: %q", got)
	}
}
