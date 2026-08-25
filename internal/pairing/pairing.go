// Package pairing holds short-lived Telegram pairing sessions.
//
// A session exists only to connect one PocketClaw install to one bot the user
// creates in Telegram. It carries no conversation content, no AI provider
// keys, and no workspace data, and it is discarded as soon as the child token
// is delivered or the session expires.
//
// Storage is behind the Store interface because this service runs on Vercel,
// where the request that creates a pairing, the Telegram webhook that
// completes it, and the request that collects the token can each execute in a
// different function instance. Process-local state cannot work; see
// README.md, "Architecture".
package pairing

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// State is the observable lifecycle of a pairing session.
type State string

const (
	// StatePending means the link has been issued and Telegram has not yet
	// reported a bot.
	StatePending State = "pending"
	// StateCreated means the manager bot saw the new child bot and its token
	// is being retrieved.
	StateCreated State = "created"
	// StateReady means the child token is held and awaiting its single
	// delivery to the app that started the pairing.
	StateReady State = "ready"
	// StateFailed means the session cannot complete; Reason says why, in
	// non-secret terms.
	StateFailed State = "failed"
)

// Failure reasons. These reach the app and must never carry secrets or
// Telegram error text that might quote a token.
const (
	ReasonTokenRetrievalFailed = "token_retrieval_failed"
)

var (
	// ErrNotFound is returned for an unknown, expired, or delivered pairing.
	ErrNotFound = errors.New("pairing not found")
	// ErrUnauthorized is returned when the poll token does not match.
	ErrUnauthorized = errors.New("poll token does not match this pairing")
	// ErrNotReady is returned when a token is requested before it exists.
	ErrNotReady = errors.New("pairing is not ready")
	// ErrConflict is returned when an identifier is already taken.
	ErrConflict = errors.New("identifier already in use")
)

// Record is one pairing session as it is stored.
//
// PollTokenMAC is a keyed digest of the poll token. The token itself is
// returned once, at creation, and is never stored: a dump of the storage
// backend yields nothing that can be replayed against this service.
type Record struct {
	ID                string `json:"id"`
	PollTokenMAC      string `json:"poll_token_mac"`
	SuggestedUsername string `json:"suggested_username"`
	SuggestedName     string `json:"suggested_name"`
	DeepLink          string `json:"deep_link"`
	State             State  `json:"state"`
	Reason            string `json:"reason,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`

	// OwnerUserID is the Telegram user that created the bot, known once
	// Telegram reports it. It becomes the child bot's allow-list entry.
	OwnerUserID int64  `json:"owner_user_id,omitempty"`
	BotUserID   int64  `json:"bot_user_id,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
}

// Store persists pairing records and child tokens.
//
// Implementations must be safe for concurrent use from separate processes.
// The child token lives under its own key so that TakeToken can be a single
// atomic operation; see PutToken.
type Store interface {
	// Create stores a new record. It must fail with ErrConflict if the id or
	// the suggested username is already taken by a live record.
	Create(ctx context.Context, rec Record) error

	// Get returns a record by id, or ErrNotFound.
	Get(ctx context.Context, id string) (Record, error)

	// FindByUsername returns the live record that suggested a username. This
	// is how a Telegram update is bound to the app that asked for it.
	FindByUsername(ctx context.Context, username string) (Record, error)

	// Update replaces an existing record, preserving its remaining lifetime.
	Update(ctx context.Context, rec Record) error

	// PutToken stores the child bot token under its own key, expiring with
	// the record.
	PutToken(ctx context.Context, id, token string) error

	// TakeToken atomically returns and removes the child token. It must be a
	// single atomic operation: this is what makes delivery exactly-once even
	// when two function instances race. It returns ErrNotFound when there is
	// nothing to take, which includes the case of a second attempt.
	TakeToken(ctx context.Context, id string) (string, error)

	// Delete removes a record and any token still held for it.
	Delete(ctx context.Context, id string) error

	// Ping reports whether the backend is reachable. Used by the setup page.
	Ping(ctx context.Context) error

	// Describe names the backend for the setup page. It must never include
	// credentials.
	Describe() string
}

// Hasher derives the stored verifier for a poll token.
//
// It is keyed with PAIRING_SECRET so that the stored value is useless to
// anyone who can read the storage backend but does not hold the key. That
// matters here because storage is a third-party service.
type Hasher struct {
	key []byte
}

// NewHasher returns a Hasher. An empty secret is rejected: falling back to an
// unkeyed digest would silently drop the property above.
func NewHasher(secret string) (*Hasher, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("PAIRING_SECRET must be at least 16 characters")
	}
	return &Hasher{key: []byte(secret)}, nil
}

// MAC returns the stored verifier for a poll token.
func (h *Hasher) MAC(pollToken string) string {
	mac := hmac.New(sha256.New, h.key)
	mac.Write([]byte(pollToken))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether a presented token matches a stored verifier, in
// constant time.
func (h *Hasher) Verify(pollToken, storedMAC string) bool {
	return hmac.Equal([]byte(h.MAC(pollToken)), []byte(storedMAC))
}

// NewID returns an unguessable pairing identifier.
func NewID() (string, error) { return randomHex(16) }

// NewPollToken returns an unguessable poll token, independent of any pairing
// identifier.
func NewPollToken() (string, error) { return randomHex(32) }

// NormalizeUsername is the key form used for username lookups.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
}

// Expired reports whether a record has outlived its window.
func (r Record) Expired(now time.Time) bool { return now.After(r.ExpiresAt) }

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
