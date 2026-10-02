# Data Model: Webhook Redeploy

Additions only. App documents, deployment records and their statuses are unchanged; webhook
deploys use the existing trigger `webhook`.

## Configuration (addition)

| Setting | Env var | Flag | Default | Rule |
| --- | --- | --- | --- | --- |
| Webhook-only listener | `CARAVANA_WEBHOOK_LISTEN` | `-webhook-listen` | empty (off) | host:port, port 1–65535, not equal to the main listen address |

## Store: migration `0002_webhook_tokens.sql`

```sql
-- At most one webhook token per app. Only a hash is stored; the token itself
-- is shown once, when it's created.
CREATE TABLE webhook_tokens (
  app_id     TEXT PRIMARY KEY REFERENCES apps (id),
  token_hash TEXT NOT NULL, -- hex SHA-256 of the token
  created_at TEXT NOT NULL  -- RFC 3339, UTC
) STRICT;
```

| Store method | Behavior |
| --- | --- |
| `SetWebhookToken(ctx, appID, hash) (WebhookTokenRecord, error)` | insert or replace; new `CreatedAt`; `ErrNotFound` for an unknown app |
| `GetWebhookToken(ctx, appID) (WebhookTokenRecord, error)` | `ErrNotFound` when the app has none (or doesn't exist) |
| `DeleteWebhookToken(ctx, appID) error` | removes it; nil when there was none; `ErrNotFound` for an unknown app |
| `LatestHealthyDeployment(ctx, appID) (DeploymentRecord, error)` | newest `healthy` record; `ErrNotFound` when none |

`WebhookTokenRecord{AppID, Hash string; CreatedAt time.Time}`.

## Token

- Form: `cvwh_` + base64url (no padding) of 32 random bytes = 48 characters.
- Stored: `hex(sha256(token))`, 64 characters.
- Check: hash the presented value, constant-time compare with the stored hash; a dummy hash when
  the app or its token is missing.

## Webhook decision (service)

```text
token valid? ── no ──► 401 unauthorized (same answer for every reason)
     │ yes
resolve every image ── fails ──────────────────────────────┐
     │ ok                                                   │
latest healthy deployment exists,                           │
stored app not updated after it,          ── no ──► Redeploy (trigger webhook) ──► 202 {changed: true}
and every digest equal?                                      │                     or 003's error
     │ yes                                                   │                     (+ Location)
200 {changed: false, deployment: latest healthy}  ◄──────────┘ (failures re-resolve and record)
```

## API resources (schemas in [contracts/openapi.yaml](contracts/openapi.yaml))

| Schema | Fields |
| --- | --- |
| `WebhookToken` (201 on create) | `token`, `path` (`/v1/webhooks/{id}?token=…`), `createdAt` |
| `WebhookStatus` | `enabled` (bool), `createdAt` (only when enabled) |
| `WebhookResult` (200/202 from the webhook) | `changed` (bool), `deployment` (feature 003's `Deployment`) |

## Error code (addition)

| Code | Status | When | Headers |
| --- | --- | --- | --- |
| `unauthorized` | 401 | missing, wrong, revoked or other-app token; app without a token; unknown app | `WWW-Authenticate: Bearer` |
