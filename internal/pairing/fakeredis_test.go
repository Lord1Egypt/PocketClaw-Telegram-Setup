package pairing

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRedis implements just enough of the Upstash REST protocol to exercise
// RedisStore against real command strings. It is not a Redis clone; it covers
// exactly the commands this service issues, including their NX/XX/KEEPTTL
// semantics, so a mistake in how a command is built shows up as a test
// failure rather than as a production outage.
type fakeRedis struct {
	mu     sync.Mutex
	values map[string]string
	expiry map[string]time.Time
	now    func() time.Time

	authToken string
	calls     []string
}

func newFakeRedis(t *testing.T) (*fakeRedis, *httptest.Server) {
	t.Helper()
	f := &fakeRedis{
		values:    make(map[string]string),
		expiry:    make(map[string]time.Time),
		now:       time.Now,
		authToken: "fake-storage-token",
	}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	return f, server
}

func (f *fakeRedis) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.authToken {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	var args []string
	if err := json.Unmarshal(body, &args); err != nil || len(args) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad command"}`))
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	f.sweepLocked()

	result, errMsg := f.executeLocked(args)
	w.Header().Set("Content-Type", "application/json")
	if errMsg != "" {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": errMsg})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

func (f *fakeRedis) executeLocked(args []string) (any, string) {
	switch strings.ToUpper(args[0]) {
	case "PING":
		return "PONG", ""

	case "GET":
		value, ok := f.values[args[1]]
		if !ok {
			return nil, ""
		}
		return value, ""

	case "GETDEL":
		value, ok := f.values[args[1]]
		if !ok {
			return nil, ""
		}
		delete(f.values, args[1])
		delete(f.expiry, args[1])
		return value, ""

	case "DEL":
		_, existed := f.values[args[1]]
		delete(f.values, args[1])
		delete(f.expiry, args[1])
		if existed {
			return 1, ""
		}
		return 0, ""

	case "TTL":
		deadline, ok := f.expiry[args[1]]
		if !ok {
			if _, exists := f.values[args[1]]; exists {
				return -1, ""
			}
			return -2, ""
		}
		return int(deadline.Sub(f.now()).Seconds()), ""

	case "SET":
		key, value := args[1], args[2]
		_, exists := f.values[key]
		var ttl int
		keepTTL, nx, xx := false, false, false
		for i := 3; i < len(args); i++ {
			switch strings.ToUpper(args[i]) {
			case "EX":
				i++
				ttl, _ = strconv.Atoi(args[i])
			case "NX":
				nx = true
			case "XX":
				xx = true
			case "KEEPTTL":
				keepTTL = true
			}
		}
		if nx && exists {
			return nil, ""
		}
		if xx && !exists {
			return nil, ""
		}
		f.values[key] = value
		if ttl > 0 {
			f.expiry[key] = f.now().Add(time.Duration(ttl) * time.Second)
		} else if !keepTTL {
			delete(f.expiry, key)
		}
		return "OK", ""
	}
	return nil, "unknown command " + args[0]
}

func (f *fakeRedis) sweepLocked() {
	now := f.now()
	for key, deadline := range f.expiry {
		if now.After(deadline) {
			delete(f.values, key)
			delete(f.expiry, key)
		}
	}
}

func (f *fakeRedis) setClock(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

func (f *fakeRedis) commandLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
