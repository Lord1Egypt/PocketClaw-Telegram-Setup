package pairing

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The two Store implementations must be indistinguishable to callers. The
// whole service correctness rests on that: development runs on memory,
// production runs on Redis, and a behavioural difference between them would
// only ever surface in production.
type storeFactory struct {
	name   string
	build  func(t *testing.T) (Store, func(time.Time))
	shared bool
}

func factories() []storeFactory {
	return []storeFactory{
		{
			name: "memory",
			build: func(t *testing.T) (Store, func(time.Time)) {
				s := NewMemoryStore()
				now := time.Now()
				s.SetClock(func() time.Time { return now })
				return s, func(at time.Time) { now = at }
			},
		},
		{
			name:   "redis",
			shared: true,
			build: func(t *testing.T) (Store, func(time.Time)) {
				fake, server := newFakeRedis(t)
				now := time.Now()
				fake.setClock(func() time.Time { return now })
				store := NewRedisStore(server.URL, "fake-storage-token", server.Client())
				return store, func(at time.Time) {
					now = at
					fake.setClock(func() time.Time { return now })
				}
			},
		},
	}
}

var ttlPattern = regexp.MustCompile(`\bEX (\d+)\b`)

func sampleRecord(id, username string, expires time.Time) Record {
	return Record{
		ID:                id,
		PollTokenMAC:      "mac-" + id,
		SuggestedUsername: username,
		SuggestedName:     "PocketClaw Agent",
		DeepLink:          "https://t.me/newbot/PocketClawSetupBot/" + username,
		State:             StatePending,
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         expires,
	}
}

func eachStore(t *testing.T, run func(t *testing.T, store Store, advance func(time.Time))) {
	for _, factory := range factories() {
		t.Run(factory.name, func(t *testing.T) {
			store, advance := factory.build(t)
			run(t, store, advance)
		})
	}
}

func TestStoreCreateAndGet(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		ctx := context.Background()
		rec := sampleRecord("abc123", "pocketclaw_aaaaaaaa_bot", time.Now().Add(10*time.Minute))
		if err := store.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := store.Get(ctx, "abc123")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.SuggestedUsername != rec.SuggestedUsername || got.State != StatePending ||
			got.PollTokenMAC != rec.PollTokenMAC || got.DeepLink != rec.DeepLink {
			t.Fatalf("round trip lost fields: %+v", got)
		}
	})
}

func TestStoreUnknownIsNotFound(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		if _, err := store.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get on unknown id returned %v, want ErrNotFound", err)
		}
	})
}

func TestStoreFindByUsernameIsCaseInsensitive(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		ctx := context.Background()
		rec := sampleRecord("abc123", "pocketclaw_aaaaaaaa_bot", time.Now().Add(10*time.Minute))
		if err := store.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		for _, probe := range []string{"POCKETCLAW_AAAAAAAA_BOT", "@pocketclaw_aaaaaaaa_bot"} {
			got, err := store.FindByUsername(ctx, probe)
			if err != nil {
				t.Fatalf("FindByUsername(%q): %v", probe, err)
			}
			if got.ID != "abc123" {
				t.Fatalf("FindByUsername(%q) returned %q", probe, got.ID)
			}
		}
	})
}

func TestStoreUsernameIsClaimedExclusively(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		ctx := context.Background()
		expires := time.Now().Add(10 * time.Minute)
		if err := store.Create(ctx, sampleRecord("one", "pocketclaw_aaaaaaaa_bot", expires)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		err := store.Create(ctx, sampleRecord("two", "pocketclaw_aaaaaaaa_bot", expires))
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("reusing a live username returned %v, want ErrConflict", err)
		}
	})
}

func TestStoreUpdatePreservesLifetime(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, advance func(time.Time)) {
		ctx := context.Background()
		start := time.Now()
		rec := sampleRecord("abc123", "pocketclaw_aaaaaaaa_bot", start.Add(10*time.Minute))
		if err := store.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		advance(start.Add(9 * time.Minute))
		rec.State = StateCreated
		rec.OwnerUserID = 555
		if err := store.Update(ctx, rec); err != nil {
			t.Fatalf("Update: %v", err)
		}

		// An update must not extend the window; the pairing still dies at the
		// original deadline.
		advance(start.Add(11 * time.Minute))
		if _, err := store.Get(ctx, "abc123"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an update extended the pairing lifetime: Get returned %v", err)
		}
	})
}

func TestStoreUpdateOfMissingRecordFails(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		rec := sampleRecord("ghost", "pocketclaw_aaaaaaaa_bot", time.Now().Add(10*time.Minute))
		if err := store.Update(context.Background(), rec); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Update of a missing record returned %v, want ErrNotFound", err)
		}
	})
}

func TestStoreTokenIsTakenExactlyOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		ctx := context.Background()
		rec := sampleRecord("abc123", "pocketclaw_aaaaaaaa_bot", time.Now().Add(10*time.Minute))
		if err := store.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := store.PutToken(ctx, "abc123", "9001:CHILD-TOKEN"); err != nil {
			t.Fatalf("PutToken: %v", err)
		}

		token, err := store.TakeToken(ctx, "abc123")
		if err != nil {
			t.Fatalf("TakeToken: %v", err)
		}
		if token != "9001:CHILD-TOKEN" {
			t.Fatalf("TakeToken returned %q", token)
		}
		if _, err := store.TakeToken(ctx, "abc123"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second TakeToken returned %v, want ErrNotFound", err)
		}
	})
}

// The exactly-once guarantee has to hold when instances race, because on
// Vercel they will.
func TestStoreConcurrentTakeYieldsOneWinner(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		ctx := context.Background()
		rec := sampleRecord("abc123", "pocketclaw_aaaaaaaa_bot", time.Now().Add(10*time.Minute))
		if err := store.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := store.PutToken(ctx, "abc123", "9001:CHILD-TOKEN"); err != nil {
			t.Fatalf("PutToken: %v", err)
		}

		const racers = 12
		var wg sync.WaitGroup
		var mu sync.Mutex
		var winners int
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				if token, err := store.TakeToken(ctx, "abc123"); err == nil && token != "" {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if winners != 1 {
			t.Fatalf("%d callers received the token; delivery must be exactly once", winners)
		}
	})
}

func TestStorePutTokenRequiresALiveRecord(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		if err := store.PutToken(context.Background(), "ghost", "9001:CHILD"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("PutToken without a record returned %v, want ErrNotFound", err)
		}
	})
}

func TestStoreExpiryRemovesRecordTokenAndUsername(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, advance func(time.Time)) {
		ctx := context.Background()
		start := time.Now()
		rec := sampleRecord("abc123", "pocketclaw_aaaaaaaa_bot", start.Add(5*time.Minute))
		if err := store.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := store.PutToken(ctx, "abc123", "9001:CHILD-TOKEN"); err != nil {
			t.Fatalf("PutToken: %v", err)
		}

		advance(start.Add(6 * time.Minute))
		if _, err := store.Get(ctx, "abc123"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired record still readable: %v", err)
		}
		if _, err := store.FindByUsername(ctx, "pocketclaw_aaaaaaaa_bot"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired username still resolves: %v", err)
		}
		if _, err := store.TakeToken(ctx, "abc123"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("token outlived its pairing: %v", err)
		}
	})
}

func TestStoreDeleteRemovesEverything(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		ctx := context.Background()
		rec := sampleRecord("abc123", "pocketclaw_aaaaaaaa_bot", time.Now().Add(10*time.Minute))
		if err := store.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := store.PutToken(ctx, "abc123", "9001:CHILD-TOKEN"); err != nil {
			t.Fatalf("PutToken: %v", err)
		}
		if err := store.Delete(ctx, "abc123"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.Get(ctx, "abc123"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("record survived Delete")
		}
		if _, err := store.TakeToken(ctx, "abc123"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("token survived Delete")
		}
		// The username must be released so a retry is not blocked by a dead
		// pairing.
		if err := store.Create(ctx, sampleRecord("new", "pocketclaw_aaaaaaaa_bot", time.Now().Add(10*time.Minute))); err != nil {
			t.Fatalf("username was not released by Delete: %v", err)
		}
	})
}

func TestStorePing(t *testing.T) {
	eachStore(t, func(t *testing.T, store Store, _ func(time.Time)) {
		if err := store.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})
}

func TestStoreDescribeCarriesNoCredential(t *testing.T) {
	_, server := newFakeRedis(t)
	store := NewRedisStore(server.URL, "fake-storage-token", server.Client())
	if strings.Contains(store.Describe(), "fake-storage-token") {
		t.Fatalf("Describe() leaks the storage credential: %q", store.Describe())
	}
}

func TestRedisStoreUsesAtomicCommands(t *testing.T) {
	fake, server := newFakeRedis(t)
	store := NewRedisStore(server.URL, "fake-storage-token", server.Client())
	ctx := context.Background()

	rec := sampleRecord("abc123", "pocketclaw_aaaaaaaa_bot", time.Now().Add(10*time.Minute))
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.PutToken(ctx, "abc123", "9001:CHILD-TOKEN"); err != nil {
		t.Fatalf("PutToken: %v", err)
	}
	if _, err := store.TakeToken(ctx, "abc123"); err != nil {
		t.Fatalf("TakeToken: %v", err)
	}

	log := strings.Join(fake.commandLog(), "\n")
	// These are the properties the design depends on. A refactor that turns
	// GETDEL into GET+DEL, or drops NX, breaks exactly-once or the username
	// claim without breaking any other test.
	if !strings.Contains(log, "GETDEL token:abc123") {
		t.Fatalf("token delivery is not a single atomic GETDEL:\n%s", log)
	}
	if !strings.Contains(log, "SET username:pocketclaw_aaaaaaaa_bot abc123 EX") || !strings.Contains(log, "NX") {
		t.Fatalf("the username claim is not atomic (SET ... NX):\n%s", log)
	}
	// Every write must carry an expiry. The exact number is the remaining
	// lifetime in whole seconds, so assert the shape rather than a constant.
	for _, command := range fake.commandLog() {
		if !strings.HasPrefix(command, "SET ") {
			continue
		}
		match := ttlPattern.FindStringSubmatch(command)
		if match == nil {
			t.Fatalf("a SET carries no EX expiry: %s", command)
		}
		seconds, err := strconv.Atoi(match[1])
		if err != nil || seconds <= 0 || seconds > 600 {
			t.Fatalf("a SET has an implausible TTL of %q: %s", match[1], command)
		}
	}
}

func TestRedisStoreRejectsBadCredentials(t *testing.T) {
	_, server := newFakeRedis(t)
	store := NewRedisStore(server.URL, "wrong-token", server.Client())
	err := store.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping succeeded with the wrong storage credential")
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("the error echoes the storage credential: %v", err)
	}
}

func TestHasherIsKeyedAndConstantTime(t *testing.T) {
	a, err := NewHasher("secret-key-that-is-long-enough")
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	b, err := NewHasher("a-different-key-of-good-length")
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}

	token := "some-poll-token"
	if a.MAC(token) == b.MAC(token) {
		t.Fatal("the digest does not depend on the key")
	}
	if strings.Contains(a.MAC(token), token) {
		t.Fatal("the digest contains the token")
	}
	if !a.Verify(token, a.MAC(token)) {
		t.Fatal("Verify rejected a correct token")
	}
	if a.Verify("other", a.MAC(token)) {
		t.Fatal("Verify accepted a wrong token")
	}
	if a.Verify(token, b.MAC(token)) {
		t.Fatal("Verify accepted a digest made with another key")
	}
}

func TestHasherRefusesAWeakSecret(t *testing.T) {
	for _, secret := range []string{"", "short"} {
		if _, err := NewHasher(secret); err == nil {
			t.Fatalf("NewHasher(%q) accepted a weak secret", secret)
		}
	}
}

func TestIdentifiersAreRandomAndIndependent(t *testing.T) {
	ids := make(map[string]bool)
	tokens := make(map[string]bool)
	for i := 0; i < 300; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		token, err := NewPollToken()
		if err != nil {
			t.Fatalf("NewPollToken: %v", err)
		}
		if len(id) != 32 {
			t.Fatalf("pairing id %q is not 32 hex chars", id)
		}
		if len(token) != 64 {
			t.Fatalf("poll token is %d chars, want 64", len(token))
		}
		if strings.Contains(token, id) || strings.Contains(id, token) {
			t.Fatal("the poll token and the pairing id are derived from each other")
		}
		ids[id] = true
		tokens[token] = true
	}
	if len(ids) != 300 || len(tokens) != 300 {
		t.Fatalf("got %d ids and %d tokens out of 300; identifiers are not random", len(ids), len(tokens))
	}
}
