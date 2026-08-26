package pairing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RedisStore is a Store backed by an Upstash-compatible Redis REST API.
//
// The REST protocol is used rather than a Redis client library so this stays a
// zero-dependency service and so it works from a serverless function with no
// connection pooling, no long-lived sockets, and no cold-start handshake.
//
// Keys, all expiring with the pairing:
//
//	pairing:{id}        the JSON record
//	username:{name}     the pairing id that suggested this username
//	token:{id}          the child bot token, taken atomically exactly once
type RedisStore struct {
	url   string
	token string
	http  *http.Client
}

// NewRedisStore returns a store for an Upstash REST endpoint.
func NewRedisStore(url, token string, httpClient *http.Client) *RedisStore {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &RedisStore{
		url:   strings.TrimSuffix(url, "/"),
		token: token,
		http:  httpClient,
	}
}

// Describe names the backend without revealing credentials.
func (s *RedisStore) Describe() string {
	host := s.url
	if parsed := strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"), "/", 2); len(parsed) > 0 {
		host = parsed[0]
	}
	return "Redis (REST) at " + host
}

func recordKey(id string) string  { return "pairing:" + id }
func usernameKey(u string) string { return "username:" + NormalizeUsername(u) }
func tokenKey(id string) string   { return "token:" + id }

// Ping verifies the backend is reachable AND writable.
//
// A bare PING is not enough. Vercel's Redis integration injects both
// KV_REST_API_TOKEN and KV_REST_API_READ_ONLY_TOKEN, whose names differ by one
// word, and a read-only token answers PING perfectly well. A reachability-only
// check would therefore report success and every pairing would then fail at
// its first write. So this writes, reads back, and deletes a scratch key.
func (s *RedisStore) Ping(ctx context.Context) error {
	var pong string
	if err := s.command(ctx, &pong, "PING"); err != nil {
		return err
	}
	if !strings.EqualFold(pong, "PONG") {
		return fmt.Errorf("unexpected PING reply %q", pong)
	}

	probe, err := randomHex(8)
	if err != nil {
		return err
	}
	key := "healthcheck:" + probe
	// A short TTL means an interrupted check cannot leave anything behind.
	if err := s.command(ctx, nil, "SET", key, probe, "EX", "60"); err != nil {
		return fmt.Errorf("%w: %s", ErrStorageNotWritable, err)
	}

	var readBack string
	if err := s.command(ctx, &readBack, "GET", key); err != nil {
		return err
	}
	_ = s.command(ctx, nil, "DEL", key)

	if readBack != probe {
		return fmt.Errorf("%w: wrote a probe key but read back %q", ErrStorageNotWritable, readBack)
	}
	return nil
}

func (s *RedisStore) Create(ctx context.Context, rec Record) error {
	ttl := int(time.Until(rec.ExpiresAt).Seconds())
	if ttl <= 0 {
		return fmt.Errorf("refusing to store an already-expired pairing")
	}
	encoded, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}

	// NX on the username claims it atomically, so two concurrent instances
	// cannot both hand out the same suggested bot username.
	var claimed any
	if err := s.command(ctx, &claimed, "SET", usernameKey(rec.SuggestedUsername), rec.ID,
		"EX", strconv.Itoa(ttl), "NX"); err != nil {
		return err
	}
	if claimed == nil {
		return ErrConflict
	}

	var stored any
	if err := s.command(ctx, &stored, "SET", recordKey(rec.ID), string(encoded),
		"EX", strconv.Itoa(ttl), "NX"); err != nil {
		return err
	}
	if stored == nil {
		// Release the username claim rather than leaving it stranded for the
		// whole TTL.
		_ = s.command(ctx, nil, "DEL", usernameKey(rec.SuggestedUsername))
		return ErrConflict
	}
	return nil
}

func (s *RedisStore) Get(ctx context.Context, id string) (Record, error) {
	var raw string
	if err := s.command(ctx, &raw, "GET", recordKey(id)); err != nil {
		return Record{}, err
	}
	if raw == "" {
		return Record{}, ErrNotFound
	}
	var rec Record
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return Record{}, fmt.Errorf("decode record: %w", err)
	}
	return rec, nil
}

func (s *RedisStore) FindByUsername(ctx context.Context, username string) (Record, error) {
	var id string
	if err := s.command(ctx, &id, "GET", usernameKey(username)); err != nil {
		return Record{}, err
	}
	if id == "" {
		return Record{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

func (s *RedisStore) Update(ctx context.Context, rec Record) error {
	encoded, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}
	// KEEPTTL: an update must not extend the pairing's lifetime. The window is
	// a security bound, not a convenience. XX: if the record expired while we
	// were working, do not resurrect it.
	var updated any
	if err := s.command(ctx, &updated, "SET", recordKey(rec.ID), string(encoded), "KEEPTTL", "XX"); err != nil {
		return err
	}
	if updated == nil {
		return ErrNotFound
	}
	return nil
}

func (s *RedisStore) PutToken(ctx context.Context, id, token string) error {
	if token == "" {
		return fmt.Errorf("refusing to store an empty child token")
	}
	var ttl int64
	if err := s.command(ctx, &ttl, "TTL", recordKey(id)); err != nil {
		return err
	}
	if ttl <= 0 {
		return ErrNotFound
	}
	return s.command(ctx, nil, "SET", tokenKey(id), token, "EX", strconv.FormatInt(ttl, 10))
}

// TakeToken uses GETDEL, which reads and removes in one server-side operation.
// Two instances racing to deliver cannot both succeed.
func (s *RedisStore) TakeToken(ctx context.Context, id string) (string, error) {
	var token string
	if err := s.command(ctx, &token, "GETDEL", tokenKey(id)); err != nil {
		return "", err
	}
	if token == "" {
		return "", ErrNotFound
	}
	return token, nil
}

func (s *RedisStore) Delete(ctx context.Context, id string) error {
	rec, err := s.Get(ctx, id)
	if err == nil {
		_ = s.command(ctx, nil, "DEL", usernameKey(rec.SuggestedUsername))
	}
	_ = s.command(ctx, nil, "DEL", tokenKey(id))
	return s.command(ctx, nil, "DEL", recordKey(id))
}

type restReply struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// command sends one Redis command over the REST API and decodes result into
// out, which may be nil to discard it.
func (s *RedisStore) command(ctx context.Context, out any, args ...string) error {
	payload, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode command: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build storage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		// A transport error can quote the URL. The URL is not a credential,
		// but the bearer token could appear in a proxy error, so the message
		// is kept generic rather than wrapping err.
		return fmt.Errorf("storage request failed: %s", redactSecret(err.Error(), s.token))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read storage response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("storage rejected the credentials (HTTP %d)", resp.StatusCode)
	}

	var reply restReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return fmt.Errorf("storage returned a non-JSON response (HTTP %d)", resp.StatusCode)
	}
	if reply.Error != "" {
		return fmt.Errorf("storage error: %s", redactSecret(reply.Error, s.token))
	}
	if out == nil {
		return nil
	}
	// A nil result decodes into the zero value, which the callers read as
	// "absent" rather than as an error.
	if len(reply.Result) == 0 || string(reply.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(reply.Result, out); err != nil {
		return fmt.Errorf("decode storage result: %w", err)
	}
	return nil
}

func redactSecret(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[REDACTED]")
}
