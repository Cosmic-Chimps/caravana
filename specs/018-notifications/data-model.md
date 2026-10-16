# Data model: Notifications

Decisions in [research.md](./research.md).

## Migration `0010_notifications.sql`

```sql
-- Feature 018: notification channels, what was sent, and the delivery queue.

-- A channel's secret parts are not here: they live in the Nomad Variable
-- caravana/notifications/<id>.
CREATE TABLE notification_channels (
  id          TEXT PRIMARY KEY,                 -- "nch_" + 12 random hex
  kind        TEXT NOT NULL CHECK (kind IN ('webhook', 'slack', 'discord', 'ntfy')),
  name        TEXT NOT NULL,
  address     TEXT NOT NULL,                    -- what the console shows (masked for slack/discord)
  events      TEXT NOT NULL,                    -- JSON ["problems","deploy_failed","deploy_succeeded"]
  paused      INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
) STRICT;

-- The needs-you items already sent as "problem opened", until resolved.
CREATE TABLE sent_problems (
  key           TEXT PRIMARY KEY,               -- kind|app|service|deployment
  kind          TEXT NOT NULL,
  app_id        TEXT NOT NULL DEFAULT '',
  service       TEXT NOT NULL DEFAULT '',
  opened_at     TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  summary       TEXT NOT NULL                   -- the title sent, reused for "resolved"
) STRICT;

CREATE TABLE notification_deliveries (
  id          TEXT PRIMARY KEY,                 -- "ndl_" + 12 random hex
  channel_id  TEXT NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
  event_type  TEXT NOT NULL,                    -- problem.opened, problem.resolved, deploy.succeeded, deploy.failed, test
  title       TEXT NOT NULL,
  payload     TEXT NOT NULL,                    -- the event JSON (no secret)
  status      TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
  attempts    INTEGER NOT NULL DEFAULT 0,
  next_at     TEXT NOT NULL,
  error       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  finished_at TEXT
) STRICT;
CREATE INDEX deliveries_due ON notification_deliveries (status, next_at);
CREATE INDEX deliveries_by_channel ON notification_deliveries (channel_id, created_at DESC);
```

Deleting an app deletes its `sent_problems` rows. Deliveries stay: they are history.

## Store

These types, with their CRUD and test calls:
- `NotificationChannel{ID, Kind, Name, Address, Events []string, Paused, CreatedAt, UpdatedAt}`;
- `SentProblem{Key, Kind, AppID, Service, OpenedAt, LastSeenAt, Summary}`;
- `Delivery{ID, ChannelID, EventType, Title, Payload, Status, Attempts, NextAt, Error, CreatedAt, FinishedAt}`.

Delivery-specific calls:
- `EnqueueDelivery`;
- `DueDeliveries(ctx, now, limit)`;
- `FinishDelivery`;
- `RetryDelivery(ctx, id, nextAt, err)`;
- `ListDeliveries(ctx, channelID, limit)`;
- `PruneDeliveries(ctx, channelID, keep)`.

## Package `internal/notify` (pure, plus HTTP sending)

- `Event{Type, Kind, AppID, Service, Title, Text, Detail, Deployment, Version *uint64, Outcome, Trigger, URL, At}`
- `Channel{Kind, Address, Secret}`: what sending needs, with the secret read from the variable.
- `Render(ch Channel, ev Event) (*http.Request, error)` is pure: it builds the request per
  research R4, including the webhook signature (`Sign(secret, t, body)`).
- `Verify(secret, header, body, now, tolerance) error`: for tests, and documented for receivers.
- `Send(ctx, client, req) error`: 10 s timeout, 2xx is ok, otherwise an error carrying the status
  and up to 200 bytes of the body. It never includes the URL's secret in the error.

## Service

- `NotificationChannels`, `CreateNotificationChannel(ctx, in)` (answers the signing secret once
  for a webhook), `UpdateNotificationChannel`, `DeleteNotificationChannel`,
  `TestNotificationChannel` (synchronous; records the delivery), `NotificationDeliveries`,
  `RotateNotificationSecret`.
- **The notifier** (research R1): `diffProblems(prev []store.SentProblem, now []AttentionItem, copiesKnown bool, at time.Time, resolveAfter time.Duration) (opened []AttentionItem, resolved []store.SentProblem, seen []string)`
  is pure. `checkProblems(ctx)` reads Attention, diffs, writes the table and enqueues.
- `notifyDeploy(rec)` is called from `finish` and `fail` (research R2).
- **The sender** (research R3): a goroutine that sends due deliveries.
- **Wording**: one function per attention kind and per deploy outcome builds the title and text,
  reusing the console's sentences where they exist (internal/ui has them for the needs-you list).
  Port them to `service` or share them, but they must not import `ui`.

## Config

- `CARAVANA_PUBLIC_URL` (`-public-url`), optional, validated as an absolute http(s) URL;
- `CARAVANA_NOTIFY_RESOLVE_AFTER` (`-notify-resolve-after`), default 5m, at least 10 s.

The installer writes `CARAVANA_PUBLIC_URL=https://<domain>`.
