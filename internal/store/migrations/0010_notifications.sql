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
