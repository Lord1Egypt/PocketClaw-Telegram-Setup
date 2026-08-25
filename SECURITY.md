# Security Policy

## Reporting a vulnerability

Please report security issues privately, not as a public issue.

Use GitHub's private vulnerability reporting on this repository
(**Security → Report a vulnerability**), which reaches the maintainer without
disclosing the problem publicly.

Include what you did, what happened, and what you expected. A proof of concept
helps. Please give a reasonable window for a fix before disclosing.

## What this service protects

The service brokers exactly two secrets, and the whole design is arranged
around them.

**The manager bot token** is a server secret. It is supplied through the
hosting platform's environment and never leaves the server: not to a browser,
not into an API response, not into a page, not into a URL, and not into a log
line. It is stripped from every error message, including transport errors that
quote the request URL. The operator status page performs no privileged action
itself — it asks the server to act.

**A child bot token** belongs to one user and is brokered once. It is held only
between Telegram issuing it and the PocketClaw app collecting it, then removed.
It never appears in a QR code, a deep link, a poll response, or a log.

## Design properties

| Property | How |
| --- | --- |
| Pairing IDs and poll tokens are unguessable | 16 and 32 bytes from `crypto/rand`, hex-encoded, independent of each other |
| A stolen storage dump cannot be replayed | Poll tokens are stored as HMAC-SHA256 keyed with `PAIRING_SECRET`, compared in constant time |
| Pairing IDs cannot be enumerated | A wrong token, an unknown pairing, and an expired pairing all answer 404 |
| Token delivery is exactly once | Atomic `GETDEL` in the store, not client-side bookkeeping; concurrent callers cannot both win |
| A failed attempt does not burn a delivery | Only a correctly authenticated call consumes the token |
| Forged webhooks are rejected | Telegram echoes `TELEGRAM_WEBHOOK_SECRET`; a constant-time comparison gates the handler before any parsing |
| Bot usernames cannot collide across instances | Atomic `SET … NX` claims the username |
| Token material is short-lived | Everything expires with the pairing TTL, ten minutes by default |
| A bot answers only its owner | Telegram reports the creating user, which becomes the bot's allow-list |

## Operator responsibilities

- **Never commit a token.** `.env` is git-ignored; keep it that way.
- **Set `TELEGRAM_WEBHOOK_SECRET` and `PAIRING_SECRET` to long random values.**
  The service refuses anything under 16 characters. `openssl rand -hex 32`.
- **Rotate a leaked manager token immediately** with `/revoke` in BotFather,
  and update the hosting environment. A token that has appeared in a
  screenshot, a chat, or a log is leaked.
- **Serve over HTTPS only.** The pairing API carries a bearer token and, once,
  a bot token.
- **Changing `PAIRING_SECRET` invalidates every live pairing.** That is the
  intended behaviour, and it is a safe thing to do if you suspect exposure.

## Deliberate non-goals

- There is **no authentication on the operator status page**. It performs
  privileged actions server-side but exposes no secret, and every action is
  idempotent and self-limiting. If you want it private, put the deployment
  behind your platform's access control.
- Rate limiting is left to the hosting platform. On Vercel, that is the
  firewall and the function concurrency limits.
