# Contract: API (`api/openapi.yaml` 0.14.0 → 0.15.0)

Additions only.

## Channels

| Operation | Route | Body | Answer |
| --- | --- | --- | --- |
| `listNotificationChannels` | `GET /v1/notifications/channels` | | 200 `[]NotificationChannel` |
| `createNotificationChannel` | `POST /v1/notifications/channels` | `NotificationChannelInput` | 201 `NotificationChannel`, plus `signingSecret` for a webhook, this once |
| `updateNotificationChannel` | `PATCH /v1/notifications/channels/{id}` | any of `name`, `url`, `token`, `events`, `paused` | 200 `NotificationChannel` |
| `deleteNotificationChannel` | `DELETE /v1/notifications/channels/{id}` | | 204 |
| `testNotificationChannel` | `POST /v1/notifications/channels/{id}/test` | | 200 `Delivery` (sent now: `sent` or `failed` with `error`) |
| `listNotificationDeliveries` | `GET /v1/notifications/channels/{id}/deliveries` | | 200 `[]Delivery`, newest first, at most 20 |
| `rotateNotificationSecret` | `POST /v1/notifications/channels/{id}/secret` | | 200 `{signingSecret}`, webhook only, else 409 `not_a_webhook` |

The schemas:

`NotificationChannelInput` takes these fields:
- `kind`: `webhook`, `slack`, `discord` or `ntfy`;
- `name`;
- `url` (the webhook URL, the Slack or Discord incoming-webhook URL, or the ntfy topic URL);
- `token` (ntfy only, optional);
- `events`: an array of `problems`, `deploy_failed` and `deploy_succeeded`, with at least one;
- `paused`.

`NotificationChannel` has `{id, kind, name, address, events, paused, hasToken, createdAt, updatedAt}`:
- `address` is the shown form;
- for slack and discord the path after the host is masked;
- never `url` for those kinds, and never `token` or `signingSecret` after creation.

`Delivery` has `{id, eventType, title, status, attempts, error?, createdAt, finishedAt?}`.

Errors:
- 422 `invalid_field` (unknown kind, a URL that isn't http(s), no events, `token` on a non-ntfy
  channel);
- 404 `not_found`.

## Webhook messages (what receivers get)

`POST <url>`, `Content-Type: application/json`

```json
{
  "id": "ndl_3f9a1c2b4d5e",
  "type": "problem.opened",
  "kind": "backup_failed",
  "app": "kuma",
  "service": "uptime-kuma",
  "title": "kuma: the backup of uptime-kuma failed",
  "text": "The volume backup at 03:00 UTC failed.",
  "detail": "upload of data: Failed to rcat: …",
  "url": "https://caravana.example.com/apps/kuma/backups",
  "at": "2026-10-17T03:00:41Z"
}
```

Deploy events add `deployment`, `version`, `outcome` (`healthy`, `failed`, `reverted`,
`superseded`) and `trigger` (`console`, `api`, `webhook`, `redeploy`).

Headers:
- `Caravana-Event: <type>`;
- `Caravana-Delivery: <id>`;
- `Caravana-Signature: t=<unix seconds>,v1=<hex>`, where v1 is HMAC-SHA256 with the channel's
  signing secret over `"<t>.<raw body>"`.

Receivers should reject a `t` older than 5 minutes.

## Parity

| Console route | Operations |
| --- | --- |
| `GET /settings` | `listTokens`, `listRegistries`, `listNotificationChannels`, `listNotificationDeliveries` |
| `POST /settings/notifications` | `createNotificationChannel` |
| `POST /settings/notifications/{id}` | `updateNotificationChannel` |
| `POST /settings/notifications/{id}/test` | `testNotificationChannel` |
| `POST /settings/notifications/{id}/secret` | `rotateNotificationSecret` |
| `POST /settings/notifications/{id}/remove` | `deleteNotificationChannel` |
