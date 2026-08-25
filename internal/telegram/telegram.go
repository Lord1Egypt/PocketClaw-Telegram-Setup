// Package telegram is a minimal Bot API client covering exactly what this
// service needs: identifying the manager bot, managing its webhook, replying
// to /start, and retrieving a managed bot's token.
//
// Managed bots are Bot API 9.6 (2026-04-03). The relevant surface is:
//
//	User.can_manage_bots         Boolean
//	Update.managed_bot           ManagedBotUpdated{user User, bot User}
//	Message.managed_bot_created  ManagedBotCreated{bot User}
//	getManagedBotToken(user_id)  -> String
//	replaceManagedBotToken(user_id) -> String
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// User is the subset of Telegram's User object this service uses.
type User struct {
	ID            int64  `json:"id"`
	IsBot         bool   `json:"is_bot"`
	FirstName     string `json:"first_name"`
	Username      string `json:"username"`
	CanManageBots bool   `json:"can_manage_bots"`
}

// Chat identifies where a message came from.
type Chat struct {
	ID int64 `json:"id"`
}

// Message is the subset of Telegram's Message object this service uses.
type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from"`
	Text      string `json:"text"`
}

// ManagedBotUpdated reports the creation of a managed bot, or a change to its
// token or owner.
type ManagedBotUpdated struct {
	User User `json:"user"`
	Bot  User `json:"bot"`
}

// Update is the subset of Telegram's Update object this service consumes.
type Update struct {
	UpdateID   int64              `json:"update_id"`
	Message    *Message           `json:"message"`
	ManagedBot *ManagedBotUpdated `json:"managed_bot"`
}

// WebhookInfo is the subset of Telegram's WebhookInfo this service reports.
type WebhookInfo struct {
	URL                  string `json:"url"`
	HasCustomCertificate bool   `json:"has_custom_certificate"`
	PendingUpdateCount   int    `json:"pending_update_count"`
	LastErrorMessage     string `json:"last_error_message"`
}

// AllowedUpdates is what this service asks Telegram to deliver. managed_bot is
// delivered by default, but naming it means a future default change cannot
// silently stop onboarding from working; message is needed only for /start.
var AllowedUpdates = []string{"managed_bot", "message"}

// Client talks to the Telegram Bot API as the manager bot.
//
// The token stays inside this package. It is never returned, logged, or
// included in an error; see Redact.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// New returns a client for the manager bot token.
func New(token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{token: token, baseURL: "https://api.telegram.org", http: httpClient}
}

// SetBaseURL points the client at a different host. For tests.
func (c *Client) SetBaseURL(base string) { c.baseURL = strings.TrimSuffix(base, "/") }

// GetMe identifies the manager bot and reports whether Telegram has granted it
// bot-management rights.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var user User
	if err := c.call(ctx, "getMe", nil, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

// SetWebhook registers the update endpoint and its secret.
//
// The secret is sent to Telegram, which echoes it back on every delivery in
// X-Telegram-Bot-Api-Secret-Token. That is what lets the webhook reject
// forged requests.
func (c *Client) SetWebhook(ctx context.Context, webhookURL, secret string) error {
	allowed, err := json.Marshal(AllowedUpdates)
	if err != nil {
		return fmt.Errorf("encode allowed_updates: %w", err)
	}
	params := url.Values{}
	params.Set("url", webhookURL)
	params.Set("secret_token", secret)
	params.Set("allowed_updates", string(allowed))
	// Old queued updates belong to a previous deployment and cannot match any
	// live pairing here.
	params.Set("drop_pending_updates", "true")
	return c.call(ctx, "setWebhook", params, nil)
}

// GetWebhookInfo reports the currently registered webhook.
func (c *Client) GetWebhookInfo(ctx context.Context) (WebhookInfo, error) {
	var info WebhookInfo
	if err := c.call(ctx, "getWebhookInfo", nil, &info); err != nil {
		return WebhookInfo{}, err
	}
	return info, nil
}

// GetManagedBotToken returns the token of a bot this manager bot manages.
// userID is the managed bot's own user identifier.
func (c *Client) GetManagedBotToken(ctx context.Context, userID int64) (string, error) {
	params := url.Values{}
	params.Set("user_id", strconv.FormatInt(userID, 10))

	var token string
	if err := c.call(ctx, "getManagedBotToken", params, &token); err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("getManagedBotToken returned an empty token for bot %d", userID)
	}
	return token, nil
}

// SendMessage sends plain text to a chat.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(chatID, 10))
	params.Set("text", text)
	params.Set("disable_web_page_preview", "true")
	return c.call(ctx, "sendMessage", params, nil)
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	endpoint := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	if params == nil {
		params = url.Values{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		bytes.NewReader([]byte(params.Encode())))
	if err != nil {
		return fmt.Errorf("build %s request: %s", method, Redact(err.Error(), c.token))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		// A transport error quotes the full URL, which contains the token.
		return fmt.Errorf("%s: %s", method, Redact(err.Error(), c.token))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%s: read response: %s", method, Redact(err.Error(), c.token))
	}

	var parsed apiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("%s: decode response (HTTP %d)", method, resp.StatusCode)
	}
	if !parsed.OK {
		return &APIError{Method: method, Code: parsed.ErrorCode, Description: Redact(parsed.Description, c.token)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(parsed.Result, out); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

// APIError is a Telegram-reported failure.
type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s failed with %d: %s", e.Method, e.Code, e.Description)
}

// Redact removes a secret from text that is about to be logged or returned.
// Bot tokens reach error strings through request URLs, so every path that can
// produce a message from a request passes through here.
func Redact(text, secret string) string {
	if secret == "" {
		return text
	}
	text = strings.ReplaceAll(text, secret, "[REDACTED]")
	// A token is "<bot id>:<secret>". Redact the secret half even when only
	// that part appears, which happens when Telegram echoes a URL.
	if idx := strings.Index(secret, ":"); idx > 0 && idx < len(secret)-1 {
		text = strings.ReplaceAll(text, secret[idx+1:], "[REDACTED]")
	}
	return text
}
