package onboarding

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/pairing"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/telegram"
)

type stubBot struct {
	me       telegram.User
	token    string
	tokenErr error
	sent     []string
}

func (s *stubBot) GetMe(context.Context) (telegram.User, error) { return s.me, nil }
func (s *stubBot) GetManagedBotToken(context.Context, int64) (string, error) {
	return s.token, s.tokenErr
}
func (s *stubBot) SendMessage(_ context.Context, _ int64, text string) error {
	s.sent = append(s.sent, text)
	return nil
}

func newService(t *testing.T, bot *stubBot) (*Service, *pairing.MemoryStore) {
	t.Helper()
	store := pairing.NewMemoryStore()
	hasher, err := pairing.NewHasher("pairing-secret-long-enough")
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(store, hasher, bot, "PocketClawSetupBot", 10*time.Minute, log), store
}

func managedBotUpdate(username string) telegram.Update {
	return telegram.Update{
		UpdateID: 1,
		ManagedBot: &telegram.ManagedBotUpdated{
			User: telegram.User{ID: 555},
			Bot:  telegram.User{ID: 9001, IsBot: true, Username: username},
		},
	}
}

func TestCreatePairingIsBrandedAndAuthenticable(t *testing.T) {
	svc, _ := newService(t, &stubBot{})
	rec, pollToken, err := svc.CreatePairing(context.Background())
	if err != nil {
		t.Fatalf("CreatePairing: %v", err)
	}
	if !strings.HasPrefix(rec.SuggestedUsername, "pocketclaw_") || !strings.HasSuffix(rec.SuggestedUsername, "_bot") {
		t.Fatalf("username = %q", rec.SuggestedUsername)
	}
	if rec.SuggestedName != "PocketClaw Agent" {
		t.Fatalf("name = %q", rec.SuggestedName)
	}
	if rec.DeepLink != "https://t.me/newbot/PocketClawSetupBot/"+rec.SuggestedUsername+"?name=PocketClaw%20Agent" {
		t.Fatalf("deep link = %q", rec.DeepLink)
	}
	// The stored record must not contain the poll token itself.
	if strings.Contains(rec.PollTokenMAC, pollToken) {
		t.Fatal("the stored verifier contains the poll token")
	}
	if _, err := svc.Authenticate(context.Background(), rec.ID, pollToken); err != nil {
		t.Fatalf("Authenticate with the right token: %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), rec.ID, "wrong"); !errors.Is(err, pairing.ErrNotFound) {
		t.Fatalf("Authenticate with a wrong token returned %v, want ErrNotFound", err)
	}
}

func TestExpiredPairingCannotAuthenticate(t *testing.T) {
	svc, _ := newService(t, &stubBot{})
	now := time.Now()
	svc.SetClock(func() time.Time { return now })

	rec, pollToken, err := svc.CreatePairing(context.Background())
	if err != nil {
		t.Fatalf("CreatePairing: %v", err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := svc.Authenticate(context.Background(), rec.ID, pollToken); !errors.Is(err, pairing.ErrNotFound) {
		t.Fatalf("an expired pairing authenticated: %v", err)
	}
}

func TestTokenRetrievalFailureIsRecordedWithoutSecrets(t *testing.T) {
	bot := &stubBot{tokenErr: &telegram.APIError{Method: "getManagedBotToken", Code: 400, Description: "Bad Request"}}
	svc, store := newService(t, bot)
	rec, pollToken, err := svc.CreatePairing(context.Background())
	if err != nil {
		t.Fatalf("CreatePairing: %v", err)
	}

	svc.HandleUpdate(context.Background(), managedBotUpdate(rec.SuggestedUsername))

	got, err := svc.Authenticate(context.Background(), rec.ID, pollToken)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.State != pairing.StateFailed || got.Reason != pairing.ReasonTokenRetrievalFailed {
		t.Fatalf("record = %+v", got)
	}
	if _, err := store.TakeToken(context.Background(), rec.ID); !errors.Is(err, pairing.ErrNotFound) {
		t.Fatal("a token was stored despite retrieval failing")
	}
	if _, _, err := svc.CollectToken(context.Background(), rec.ID, pollToken); !errors.Is(err, pairing.ErrNotReady) {
		t.Fatalf("CollectToken after failure returned %v, want ErrNotReady", err)
	}
}

func TestUpdateForAnExpiredPairingIsIgnored(t *testing.T) {
	bot := &stubBot{token: "9001:CHILD"}
	svc, store := newService(t, bot)
	now := time.Now()
	svc.SetClock(func() time.Time { return now })
	store.SetClock(func() time.Time { return now.Add(-1 * time.Second) })

	rec, _, err := svc.CreatePairing(context.Background())
	if err != nil {
		t.Fatalf("CreatePairing: %v", err)
	}
	// The service clock is past the deadline while the store still holds the
	// record, which is the window where a late webhook could otherwise land.
	now = now.Add(11 * time.Minute)
	svc.HandleUpdate(context.Background(), managedBotUpdate(rec.SuggestedUsername))

	if _, err := store.TakeToken(context.Background(), rec.ID); !errors.Is(err, pairing.ErrNotFound) {
		t.Fatal("a late webhook completed an expired pairing")
	}
}

func TestStartReplyIsBranded(t *testing.T) {
	bot := &stubBot{}
	svc, _ := newService(t, bot)
	svc.HandleUpdate(context.Background(), telegram.Update{
		UpdateID: 2,
		Message:  &telegram.Message{MessageID: 1, Chat: telegram.Chat{ID: 42}, Text: "/start@PocketClawSetupBot"},
	})
	if len(bot.sent) != 1 {
		t.Fatalf("sent %d replies, want 1", len(bot.sent))
	}
	if !strings.Contains(bot.sent[0], "PocketClaw Setup") {
		t.Fatalf("reply = %q", bot.sent[0])
	}
}

func TestUnknownCommandGetsNoReply(t *testing.T) {
	bot := &stubBot{}
	svc, _ := newService(t, bot)
	svc.HandleUpdate(context.Background(), telegram.Update{
		UpdateID: 3,
		Message:  &telegram.Message{MessageID: 1, Chat: telegram.Chat{ID: 42}, Text: "hello there"},
	})
	if len(bot.sent) != 0 {
		t.Fatalf("the manager bot replied to an unrelated message: %v", bot.sent)
	}
}

func TestVerifyManagerRequiresBothIdentityAndRights(t *testing.T) {
	cases := []struct {
		name    string
		me      telegram.User
		wantErr string
	}{
		{"wrong bot", telegram.User{Username: "SomeOtherBot", CanManageBots: true}, "does not match"},
		{"management off", telegram.User{Username: "PocketClawSetupBot"}, "Bot Management Mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t, &stubBot{me: tc.me})
			_, err := svc.VerifyManager(context.Background())
			if err == nil {
				t.Fatal("VerifyManager accepted an unusable manager")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyManagerAcceptsAProperManager(t *testing.T) {
	svc, _ := newService(t, &stubBot{me: telegram.User{ID: 1, IsBot: true, Username: "PocketClawSetupBot", CanManageBots: true}})
	me, err := svc.VerifyManager(context.Background())
	if err != nil {
		t.Fatalf("VerifyManager: %v", err)
	}
	if !me.CanManageBots {
		t.Fatal("can_manage_bots was not reported")
	}
}

func TestEachPairingGetsADistinctUsername(t *testing.T) {
	svc, _ := newService(t, &stubBot{})
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		rec, _, err := svc.CreatePairing(context.Background())
		if err != nil {
			t.Fatalf("CreatePairing: %v", err)
		}
		if seen[rec.SuggestedUsername] {
			t.Fatalf("username %q was suggested twice while both pairings were live", rec.SuggestedUsername)
		}
		seen[rec.SuggestedUsername] = true
	}
}
