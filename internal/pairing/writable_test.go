package pairing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readOnlyRedis answers reads but refuses writes, which is how a read-only
// credential behaves. Vercel's Redis integration injects both
// KV_REST_API_TOKEN and KV_REST_API_READ_ONLY_TOKEN, so this is a mistake an
// operator can plausibly make in one click.
func readOnlyRedis(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 512)
		n, _ := r.Body.Read(body)
		command := strings.ToUpper(string(body[:n]))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(command, `"PING"`):
			_, _ = w.Write([]byte(`{"result":"PONG"}`))
		case strings.Contains(command, `"SET"`):
			_, _ = w.Write([]byte(`{"error":"ERR this user has no permissions to run the 'set' command"}`))
		default:
			_, _ = w.Write([]byte(`{"result":null}`))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPingRejectsAReadOnlyCredential(t *testing.T) {
	server := readOnlyRedis(t)
	store := NewRedisStore(server.URL, "read-only-token", server.Client())

	err := store.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping reported success against a read-only credential; " +
			"the storage check would go green and every pairing would then fail")
	}
	if !errors.Is(err, ErrStorageNotWritable) {
		t.Fatalf("Ping returned %v, want ErrStorageNotWritable", err)
	}
}

func TestPingVerifiesAWriteRoundTrip(t *testing.T) {
	fake, server := newFakeRedis(t)
	store := NewRedisStore(server.URL, "fake-storage-token", server.Client())

	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("Ping against a healthy store: %v", err)
	}

	log := strings.Join(fake.commandLog(), "\n")
	for _, expected := range []string{"PING", "SET healthcheck:", "GET healthcheck:", "DEL healthcheck:"} {
		if !strings.Contains(log, expected) {
			t.Fatalf("Ping did not issue %q:\n%s", expected, log)
		}
	}
}

// A health check must not accumulate keys in the operator's database.
func TestPingLeavesNothingBehind(t *testing.T) {
	fake, server := newFakeRedis(t)
	store := NewRedisStore(server.URL, "fake-storage-token", server.Client())

	for i := 0; i < 5; i++ {
		if err := store.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for key := range fake.values {
		if strings.HasPrefix(key, "healthcheck:") {
			t.Fatalf("Ping left %q behind", key)
		}
	}
}
