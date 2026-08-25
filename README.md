# PocketClaw Telegram Setup

The manager service behind PocketClaw's Telegram onboarding. It turns
"create a bot in BotFather, copy the token, paste it into the app" into
"tap Open Telegram, confirm, done".

[![Deploy with Vercel](https://vercel.com/button)](https://vercel.com/new/clone?repository-url=https%3A%2F%2Fgithub.com%2FLord1Egypt%2FPocketClaw-Telegram-Setup&env=TELEGRAM_MANAGER_BOT_TOKEN%2CTELEGRAM_MANAGER_BOT_USERNAME%2CTELEGRAM_WEBHOOK_SECRET%2CPAIRING_SECRET%2CKV_REST_API_URL%2CKV_REST_API_TOKEN&envDescription=Manager%20bot%20token%20and%20username%20from%20BotFather%2C%20two%20long%20random%20strings%20you%20invent%2C%20and%20the%20REST%20URL%20and%20token%20for%20a%20Redis-compatible%20key%2Fvalue%20store.&envLink=https%3A%2F%2Fgithub.com%2FLord1Egypt%2FPocketClaw-Telegram-Setup%23environment-variables&project-name=pocketclaw-telegram-setup&repository-name=pocketclaw-telegram-setup)

---

## What it does

PocketClaw connects to Telegram through a bot that belongs to you. Getting that
bot normally means a detour through [@BotFather](https://t.me/BotFather):
`/newbot`, pick a name, invent a username that is not taken, copy a long
secret token, switch back to the app, paste it in.

This service removes that detour using Telegram's official
[Managed Bots](https://core.telegram.org/bots/api#getmanagedbottoken) support
(Bot API 9.6, April 2026). PocketClaw asks this service for a pairing; the
service hands back a Telegram link with the bot's name and username already
filled in. You confirm in Telegram, and the token is delivered straight to your
PocketClaw install. You never see it.

**Nobody copies a token, and nobody but you ever holds one.**

## How it works

```
PocketClaw app          this service              Telegram
     │                       │                        │
     │ POST /telegram/pairings                        │
     ├──────────────────────>│                        │
     │  pairing_id, poll_token, deep_link, qr_payload │
     │<──────────────────────┤                        │
     │                       │                        │
     │  open deep link / show QR ────────────────────>│
     │                       │    you confirm the     │
     │                       │   pre-filled new bot   │
     │                       │                        │
     │                       │<─ webhook: managed_bot ┤
     │                       │  getManagedBotToken    │
     │                       ├───────────────────────>│
     │ GET  /telegram/pairings/{id}   → state: ready  │
     ├──────────────────────>│                        │
     │ POST /telegram/pairings/{id}/token             │
     ├──────────────────────>│  single use, then the  │
     │  bot_token, bot_username, owner_user_id        │
     │<──────────────────────┤  session is destroyed  │
```

Telegram still shows its own confirmation screen and you still press Create.
This service pre-fills the name and username; it cannot create a bot without
your confirmation.

## Deploy to Vercel

[![Deploy with Vercel](https://vercel.com/button)](https://vercel.com/new/clone?repository-url=https%3A%2F%2Fgithub.com%2FLord1Egypt%2FPocketClaw-Telegram-Setup&env=TELEGRAM_MANAGER_BOT_TOKEN%2CTELEGRAM_MANAGER_BOT_USERNAME%2CTELEGRAM_WEBHOOK_SECRET%2CPAIRING_SECRET%2CKV_REST_API_URL%2CKV_REST_API_TOKEN&envDescription=Manager%20bot%20token%20and%20username%20from%20BotFather%2C%20two%20long%20random%20strings%20you%20invent%2C%20and%20the%20REST%20URL%20and%20token%20for%20a%20Redis-compatible%20key%2Fvalue%20store.&envLink=https%3A%2F%2Fgithub.com%2FLord1Egypt%2FPocketClaw-Telegram-Setup%23environment-variables&project-name=pocketclaw-telegram-setup&repository-name=pocketclaw-telegram-setup)

1. **Click the button.** Vercel forks this repository into your account and
   asks for the environment variables below.
2. **Add a Redis store.** In your new project: *Storage → Create Database →
   Redis* (Upstash). Vercel injects `KV_REST_API_URL` and
   `KV_REST_API_TOKEN` automatically. If you already have an Upstash database,
   paste its REST URL and token instead.
3. **Redeploy** so the function picks up the variables.
4. **Open your deployment URL.** You get a status page.
5. **Click Register Webhook**, then **Verify Telegram**, then **Check Storage**.
   All three should turn green.
6. **Point PocketClaw at it** by building the app with
   `--dart-define=POCKETCLAW_ONBOARDING_BASE_URL=https://your-deployment`.

The deployed repository may be public — nothing in this repository is secret,
and every credential lives in Vercel's environment, not in the code.

## Create the Manager Bot

The service acts through a Telegram bot that is allowed to create other bots on
your behalf. You need one before anything works.

1. Open [@BotFather](https://t.me/BotFather) and send `/newbot`.
2. Give it a name and a username. The official PocketClaw manager is
   **PocketClaw Setup** / **@PocketClawSetupBot** — pick your own if you are
   running your own instance; nothing in the code hardcodes it.
3. Open **BotFather's mini app**, select your bot, and enable
   **Bot Management Mode**. This is what makes Telegram set
   `can_manage_bots` on the bot. Without it, the links this service generates
   will open Telegram but Telegram will not offer to create a bot.
4. Copy the token BotFather gives you.
5. Paste it **directly into Vercel's Environment Variables** as
   `TELEGRAM_MANAGER_BOT_TOKEN`. Not into a file, not into the repository,
   not into a chat message.

If the token is ever exposed, revoke it with `/revoke` in BotFather and put
the new one into Vercel.

### Recommended BotFather configuration

| Field | Value |
| --- | --- |
| Name | `PocketClaw Setup` |
| Username | `@PocketClawSetupBot` |
| About | `Creates and securely connects your personal Telegram bot to PocketClaw.` |
| Description | `PocketClaw Setup helps you create and connect your personal PocketClaw Telegram bot.`<br>``<br>`Start from the PocketClaw app, open the Telegram setup link or scan the QR code, confirm your new bot, and PocketClaw will complete the connection automatically.` |
| Commands | `start - Show PocketClaw Telegram setup information`<br>`help - Show setup help` |
| Privacy Policy | `https://<your-deployment>/privacy` |

Set the privacy policy URL after you deploy, once you know your domain.

## Environment Variables

| Variable | Secret | Required | Description |
| --- | --- | --- | --- |
| `TELEGRAM_MANAGER_BOT_TOKEN` | **yes** | yes | Manager bot token from BotFather. |
| `TELEGRAM_MANAGER_BOT_USERNAME` | no | yes | The manager's `@username`, without the `@`. Public — it appears in every link. |
| `TELEGRAM_WEBHOOK_SECRET` | **yes** | yes | A long random string you invent. Telegram echoes it on every delivery, which is how forged webhooks are rejected. |
| `PAIRING_SECRET` | **yes** | yes | A long random string you invent. Keys the stored poll-token verifier. |
| `KV_REST_API_URL` | no | yes | Redis REST endpoint. Provisioned automatically by Vercel's Redis integration. |
| `KV_REST_API_TOKEN` | **yes** | yes | Redis REST token. |
| `PUBLIC_BASE_URL` | no | no | Override the public origin. Only needed behind a custom domain or proxy. |
| `PAIRING_TTL_SECONDS` | no | no | Pairing lifetime. Default `600`. |
| `ALLOW_MEMORY_STORE` | no | no | Permit the in-memory store. **Local development only.** |

`UPSTASH_REDIS_REST_URL` and `UPSTASH_REDIS_REST_TOKEN` are accepted as
aliases for the two `KV_REST_API_*` variables.

Generate the two random strings with:

```bash
openssl rand -hex 32
```

## Security

**No secret belongs in Git.** This repository contains none, and the
`.gitignore` keeps `.env` out. Every credential is supplied through the
hosting platform's environment.

- **The manager bot token never leaves the server.** The setup page has buttons
  that *ask the server* to verify Telegram and register the webhook; it is
  never handed a token to act with. No API response, page, log line, or URL
  contains it, and it is stripped from every error message — including
  transport errors, which quote the request URL and therefore the token.
- **The webhook is authenticated.** Telegram echoes
  `TELEGRAM_WEBHOOK_SECRET` in `X-Telegram-Bot-Api-Secret-Token` on every
  delivery. Requests without it, or with the wrong value, are rejected with
  403 before anything is parsed.
- **Poll tokens are stored keyed-hashed, never in plaintext.** A dump of the
  storage backend cannot be replayed against this service.
- **A wrong token is indistinguishable from an unknown pairing.** Both answer
  404, so live pairing identifiers cannot be enumerated.
- **Token delivery is exactly once,** enforced by an atomic `GETDEL` in the
  store rather than by client behaviour. Two function instances racing cannot
  both deliver. A failed attempt with the wrong token does not burn the
  delivery.
- **No secret is ever in a URL, a QR code, or a deep link.** The QR contains
  only the public Telegram creation link.
- **The child bot token exists server-side for seconds** — between retrieval
  and delivery — and is wiped on delivery, on failure, and on expiry.
- **Child bots are locked to their creator.** Telegram tells the service which
  user made the bot, and PocketClaw writes that ID into the bot's allow-list,
  so a freshly paired bot answers only its owner.

Found a vulnerability? See [SECURITY.md](SECURITY.md).

### Correlation, and its one limitation

An incoming `managed_bot` update is matched to a pairing by the child bot's
username, which is why each pairing suggests a unique random one. If you edit
the suggested username on Telegram's confirmation screen, the update matches no
pairing and the session expires. That is deliberate: matching loosely — say, to
"the only pending pairing" — would let one person's bot be delivered into
another person's app. PocketClaw shows the expiry and offers a retry.

## Privacy

See [PRIVACY.md](PRIVACY.md), served live at `/privacy` on any deployment.

In short: this service handles a pairing and nothing else. It never sees AI
conversations, AI provider API keys, PocketClaw workspace files, or any message
sent to your bot. Pairing records live in a key/value store with a ten-minute
expiry, and completed records are deleted the moment the token is delivered.

## Local Development

```bash
go test ./...        # no network, no secrets, no Telegram account needed
go build ./...
```

Every test drives a fake Telegram and a fake Redis. Nothing requires a real
manager bot or a production credential.

To run the server:

```bash
cp .env.example .env    # fill it in
set -a && source .env && set +a
go run ./cmd/server
open http://localhost:3000
```

With `ALLOW_MEMORY_STORE=true` and no Redis variables, the service runs
against an in-memory store. That is fine on a laptop and wrong everywhere else;
the setup page says so.

## API

### `POST /telegram/pairings`

Starts a pairing.

```json
{
  "pairing_id": "<32 hex chars>",
  "poll_token": "<64 hex chars>",
  "suggested_username": "pocketclaw_k7m2x9aa_bot",
  "suggested_name": "PocketClaw Agent",
  "deep_link": "https://t.me/newbot/PocketClawSetupBot/pocketclaw_k7m2x9aa_bot?name=PocketClaw%20Agent",
  "qr_payload": "https://t.me/newbot/PocketClawSetupBot/pocketclaw_k7m2x9aa_bot?name=PocketClaw%20Agent",
  "expires_at": "2026-08-26T12:34:56Z",
  "poll_interval_seconds": 2
}
```

`poll_token` is returned here and never again. `qr_payload` is deliberately
identical to `deep_link`.

### `GET /telegram/pairings/{pairing_id}`

`Authorization: Bearer <poll_token>`. **Never returns a token.**

```json
{ "pairing_id": "...", "state": "pending", "expires_at": "..." }
```

`state` is one of `pending`, `created`, `ready`, `failed`. An expired,
unknown, or wrongly-authenticated pairing answers 404.

### `POST /telegram/pairings/{pairing_id}/token`

`Authorization: Bearer <poll_token>`. Delivers the child token **exactly
once**, then destroys the session.

```json
{
  "bot_token": "9001:...",
  "bot_user_id": 9001,
  "bot_username": "pocketclaw_k7m2x9aa_bot",
  "owner_user_id": 555
}
```

### Other endpoints

| Path | Purpose |
| --- | --- |
| `POST /telegram/webhook` | Telegram delivery endpoint; requires the shared secret. |
| `GET /` | Operator status page. |
| `GET /privacy` | Privacy notice. |
| `GET /healthz` | Liveness. |
| `POST /api/verify-telegram` | Server-side `getMe`, checks `can_manage_bots`. |
| `POST /api/register-webhook` | Server-side `setWebhook`. |
| `POST /api/check-storage` | Storage reachability. |
| `POST /api/test-pairing` | Creates and deletes a throwaway pairing. |

## Architecture

```
api/index.go          Vercel entrypoint; vercel.json rewrites every path here
cmd/server/main.go    the same wiring, as a local server
internal/app          builds the stack from the environment
internal/config       environment loading and validation
internal/httpapi      routing, the pairing API, the webhook, the operator pages
internal/onboarding   pairing lifecycle and Telegram correlation
internal/pairing      the Store interface, Redis and in-memory implementations
internal/telegram     minimal Bot API client
internal/naming       child bot identity generation and Telegram username rules
internal/deeplink     the t.me/newbot link
```

**Zero external dependencies.** The whole service is standard library. There is
no `go.sum` because there is nothing to sum.

### Why shared storage, not memory

On Vercel, the request that creates a pairing, the Telegram webhook that
completes it, and the request that collects the token can each execute in a
**different function instance**. A Go map would work in development and fail in
production in a way that looks intermittent. So state lives in Redis, keyed by
pairing, with the TTL doing the expiry.

Two operations must be atomic, and both are done server-side by Redis rather
than in application code:

- **Claiming a suggested username** — `SET username:… NX`, so two instances
  cannot hand out the same bot username.
- **Delivering the child token** — `GETDEL token:…`, so exactly one caller
  can ever receive it.

### Why webhook, not polling

`getUpdates` long-polling needs a process that stays alive. Serverless
functions do not. Telegram pushes to `/telegram/webhook` instead, and the
shared secret is what makes that endpoint safe to expose.

## License

[MIT](LICENSE) © 2026 Lord1Egypt

PocketClaw is the client this service exists for; it is developed separately.
