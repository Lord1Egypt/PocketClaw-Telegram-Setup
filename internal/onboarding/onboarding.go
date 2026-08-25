// Package onboarding ties Telegram's managed-bot updates to the pairing
// sessions that asked for them.
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/deeplink"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/naming"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/pairing"
	"github.com/Lord1Egypt/PocketClaw-Telegram-Setup/internal/telegram"
)

// BotAPI is the slice of the Telegram client this package needs. It is an
// interface so tests can drive the whole flow without a network.
type BotAPI interface {
	GetMe(ctx context.Context) (telegram.User, error)
	GetManagedBotToken(ctx context.Context, userID int64) (string, error)
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// ManagerStartReply is what the manager bot answers to /start. It explains
// what the bot is for and nothing about the service's internals.
const ManagerStartReply = "PocketClaw Setup 🦞\n\n" +
	"This bot securely creates and connects personal Telegram bots for PocketClaw.\n\n" +
	"Start from the PocketClaw app and choose Connect Telegram."

// Service creates pairings and resolves them from Telegram updates.
type Service struct {
	store           pairing.Store
	hasher          *pairing.Hasher
	bot             BotAPI
	managerUsername string
	ttl             time.Duration
	log             *slog.Logger

	createAttempts int
	now            func() time.Time
}

// New returns a Service.
func New(store pairing.Store, hasher *pairing.Hasher, bot BotAPI, managerUsername string, ttl time.Duration, log *slog.Logger) *Service {
	return &Service{
		store:           store,
		hasher:          hasher,
		bot:             bot,
		managerUsername: managerUsername,
		ttl:             ttl,
		log:             log,
		createAttempts:  5,
		now:             time.Now,
	}
}

// SetClock replaces the service clock. For tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// ManagerUsername reports the configured manager bot username.
func (s *Service) ManagerUsername() string { return s.managerUsername }

// CreatePairing issues a new pairing session and returns it along with the
// poll token, which is produced exactly once here and never stored.
func (s *Service) CreatePairing(ctx context.Context) (pairing.Record, string, error) {
	pollToken, err := pairing.NewPollToken()
	if err != nil {
		return pairing.Record{}, "", err
	}

	var lastErr error
	for attempt := 0; attempt < s.createAttempts; attempt++ {
		username, err := naming.GenerateUsername()
		if err != nil {
			return pairing.Record{}, "", err
		}
		link, err := deeplink.NewBot(s.managerUsername, username, naming.DefaultDisplayName)
		if err != nil {
			return pairing.Record{}, "", err
		}
		id, err := pairing.NewID()
		if err != nil {
			return pairing.Record{}, "", err
		}

		now := s.now().UTC()
		rec := pairing.Record{
			ID:                id,
			PollTokenMAC:      s.hasher.MAC(pollToken),
			SuggestedUsername: username,
			SuggestedName:     naming.DefaultDisplayName,
			DeepLink:          link,
			State:             pairing.StatePending,
			CreatedAt:         now,
			ExpiresAt:         now.Add(s.ttl),
		}

		err = s.store.Create(ctx, rec)
		if errors.Is(err, pairing.ErrConflict) {
			// Astronomically unlikely; retrying is cheaper than failing.
			lastErr = err
			continue
		}
		if err != nil {
			return pairing.Record{}, "", err
		}
		return rec, pollToken, nil
	}
	return pairing.Record{}, "", fmt.Errorf("could not allocate a free bot username: %w", lastErr)
}

// Authenticate returns a pairing for a caller holding its poll token.
//
// An unknown pairing and a wrong token are the same error on purpose, so the
// caller cannot use this to discover which pairing ids exist.
func (s *Service) Authenticate(ctx context.Context, id, pollToken string) (pairing.Record, error) {
	rec, err := s.store.Get(ctx, id)
	if err != nil {
		return pairing.Record{}, pairing.ErrNotFound
	}
	if rec.Expired(s.now().UTC()) {
		return pairing.Record{}, pairing.ErrNotFound
	}
	if !s.hasher.Verify(pollToken, rec.PollTokenMAC) {
		return pairing.Record{}, pairing.ErrNotFound
	}
	return rec, nil
}

// CollectToken delivers the child token exactly once.
//
// Exactly-once is enforced by the store's atomic take, not by anything here,
// so two instances racing cannot both deliver.
func (s *Service) CollectToken(ctx context.Context, id, pollToken string) (pairing.Record, string, error) {
	rec, err := s.Authenticate(ctx, id, pollToken)
	if err != nil {
		return pairing.Record{}, "", err
	}
	if rec.State != pairing.StateReady {
		return pairing.Record{}, "", pairing.ErrNotReady
	}

	token, err := s.store.TakeToken(ctx, id)
	if errors.Is(err, pairing.ErrNotFound) {
		// Already delivered, or the record outlived its token.
		return pairing.Record{}, "", pairing.ErrNotFound
	}
	if err != nil {
		return pairing.Record{}, "", err
	}

	// The session has served its purpose; drop everything about it.
	if err := s.store.Delete(ctx, id); err != nil {
		s.log.Warn("could not delete a delivered pairing", "pairing_id", id, "error", err)
	}
	return rec, token, nil
}

// HandleUpdate resolves one Telegram update.
func (s *Service) HandleUpdate(ctx context.Context, update telegram.Update) {
	switch {
	case update.ManagedBot != nil:
		s.handleManagedBot(ctx, update.ManagedBot)
	case update.Message != nil:
		s.handleMessage(ctx, update.Message)
	}
}

func (s *Service) handleManagedBot(ctx context.Context, created *telegram.ManagedBotUpdated) {
	rec, err := s.store.FindByUsername(ctx, created.Bot.Username)
	if err != nil {
		// Normal: a manager bot sees bots created outside any pairing this
		// deployment issued, and pairings expire.
		s.log.Info("ignoring a managed-bot update with no live pairing",
			"bot_username", created.Bot.Username)
		return
	}
	if rec.Expired(s.now().UTC()) {
		s.log.Info("ignoring a managed-bot update for an expired pairing", "pairing_id", rec.ID)
		return
	}

	rec.State = pairing.StateCreated
	rec.OwnerUserID = created.User.ID
	rec.BotUserID = created.Bot.ID
	rec.BotUsername = created.Bot.Username
	if err := s.store.Update(ctx, rec); err != nil {
		s.log.Error("could not mark pairing created", "pairing_id", rec.ID, "error", err)
		return
	}
	s.log.Info("managed bot created",
		"pairing_id", rec.ID, "bot_username", created.Bot.Username, "bot_id", created.Bot.ID)

	token, err := s.bot.GetManagedBotToken(ctx, created.Bot.ID)
	if err != nil {
		// err is already redacted by the telegram client. Never log the token.
		s.log.Error("could not retrieve the managed bot token",
			"pairing_id", rec.ID, "bot_id", created.Bot.ID, "error", err)
		s.fail(ctx, rec, pairing.ReasonTokenRetrievalFailed)
		return
	}
	if err := s.store.PutToken(ctx, rec.ID, token); err != nil {
		s.log.Error("could not store the child token", "pairing_id", rec.ID, "error", err)
		s.fail(ctx, rec, pairing.ReasonTokenRetrievalFailed)
		return
	}

	rec.State = pairing.StateReady
	if err := s.store.Update(ctx, rec); err != nil {
		s.log.Error("could not mark pairing ready", "pairing_id", rec.ID, "error", err)
		return
	}
	s.log.Info("pairing ready for delivery", "pairing_id", rec.ID)
}

func (s *Service) handleMessage(ctx context.Context, msg *telegram.Message) {
	if msg.Chat.ID == 0 {
		return
	}
	command, _, _ := strings.Cut(msg.Text, " ")
	command, _, _ = strings.Cut(command, "@")
	switch command {
	case "/start", "/help":
		if err := s.bot.SendMessage(ctx, msg.Chat.ID, ManagerStartReply); err != nil {
			s.log.Error("could not answer /start", "error", err)
		}
	}
}

func (s *Service) fail(ctx context.Context, rec pairing.Record, reason string) {
	rec.State = pairing.StateFailed
	rec.Reason = reason
	if err := s.store.Update(ctx, rec); err != nil {
		s.log.Error("could not mark pairing failed", "pairing_id", rec.ID, "error", err)
	}
}

// VerifyManager checks that the configured manager bot exists, matches the
// configured username, and has bot-management rights.
func (s *Service) VerifyManager(ctx context.Context) (telegram.User, error) {
	me, err := s.bot.GetMe(ctx)
	if err != nil {
		return telegram.User{}, err
	}
	if !strings.EqualFold(me.Username, s.managerUsername) {
		return me, fmt.Errorf("the configured username @%s does not match the bot behind the token, which is @%s",
			s.managerUsername, me.Username)
	}
	if !me.CanManageBots {
		return me, fmt.Errorf("manager bot @%s does not have bot management enabled; "+
			"turn on Bot Management Mode in the BotFather mini app", me.Username)
	}
	return me, nil
}
