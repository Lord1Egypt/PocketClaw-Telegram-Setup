# Privacy

**PocketClaw Telegram Setup** — the manager service behind PocketClaw's
Telegram onboarding.

This service exists to connect one PocketClaw installation to one Telegram bot
that you create and own. It is deliberately narrow: it handles a pairing, and
nothing else.

This document describes what the software does. Each deployment is operated
independently by whoever deployed it; running it is not a promise made by the
authors.

The same notice is served live at `/privacy` on any deployment.

## What it temporarily processes

- A randomly generated **pairing identifier**.
- A **keyed digest of the pairing's poll token**. The token itself is returned
  once to your PocketClaw app and is never stored.
- The **Telegram user ID** of the person who creates the bot. It is used to
  restrict the new bot to its owner.
- The **generated child bot identity**: its suggested name, its suggested
  username, and — once created — its Telegram bot ID and final username.
- The **child bot token**, held only between the moment Telegram issues it and
  the moment it is delivered to your PocketClaw app.
- The **pairing state**: `pending`, `created`, `ready`, or `failed`.

## What it does not process

- AI conversations, prompts, or responses.
- AI provider API keys.
- PocketClaw workspace files.
- Message content from your bot. This service is not your bot; it only creates
  it, and it is not in the path of anything your bot later does.

## Retention

Pairing records are stored in a Redis-compatible key/value store with an expiry
set at creation — **ten minutes by default**, configurable through
`PAIRING_TTL_SECONDS`.

- When a pairing completes, the child bot token is removed at the moment it is
  delivered, and the pairing record is deleted immediately afterwards.
- When token retrieval fails, the record is marked failed and holds no token.
- Pairings that are never completed expire on their own and are removed by the
  store.

Records are not archived, exported, or used for analytics. There is no user
account and no long-term profile. Nothing is written to disk by the service
itself.

## Logs

Operational logs record pairing identifiers, bot usernames, and Telegram bot
IDs so that failures can be diagnosed.

They never record bot tokens, poll tokens, or the manager bot's credentials.
Secrets are stripped before any error is logged, including transport errors
that would otherwise quote a request URL containing a token.

Logs are retained by the hosting platform under its own retention policy, which
is outside this software's control.

## Third parties

A deployment necessarily involves:

- **Telegram**, which creates the bot and issues its token. Telegram's own
  privacy policy applies to everything that happens inside Telegram.
- **The hosting platform** (Vercel, in the documented setup), which serves the
  requests and retains logs.
- **The key/value store** (Upstash, or whatever Redis-compatible endpoint the
  operator configures), which holds pairing records for their TTL. This is why
  the poll-token verifier is keyed with a server-side secret: the store holds
  no value that is usable without it.

No other third party receives anything.

## Contact

Issues and questions: <https://github.com/Lord1Egypt/PocketClaw-Telegram-Setup/issues>.
For anything sensitive, see [SECURITY.md](SECURITY.md).
