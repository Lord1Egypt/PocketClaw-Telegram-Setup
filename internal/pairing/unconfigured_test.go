package pairing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The whole point of this store is that it never pretends to work. A silent
// in-memory fallback would let a storage-less deployment create pairings that
// can never complete, because the Telegram webhook lands in a different
// function instance.
func TestUnconfiguredStoreRefusesEveryOperation(t *testing.T) {
	store := NewUnconfiguredStore()
	ctx := context.Background()
	rec := Record{ID: "abc", SuggestedUsername: "pocketclaw_aaaaaaaa_bot", ExpiresAt: time.Now().Add(time.Minute)}

	checks := map[string]error{
		"Ping":     store.Ping(ctx),
		"Create":   store.Create(ctx, rec),
		"Update":   store.Update(ctx, rec),
		"PutToken": store.PutToken(ctx, "abc", "token"),
		"Delete":   store.Delete(ctx, "abc"),
	}
	_, checks["Get"] = store.Get(ctx, "abc")
	_, checks["FindByUsername"] = store.FindByUsername(ctx, "pocketclaw_aaaaaaaa_bot")
	_, checks["TakeToken"] = store.TakeToken(ctx, "abc")

	for name, err := range checks {
		if !errors.Is(err, ErrStorageNotConfigured) {
			t.Fatalf("%s returned %v, want ErrStorageNotConfigured", name, err)
		}
	}
}

func TestUnconfiguredStoreSaysSo(t *testing.T) {
	if got := NewUnconfiguredStore().Describe(); got != "Not configured" {
		t.Fatalf("Describe() = %q", got)
	}
}
