-- Email and Telegram notification channels. A channel may now be of kind
-- 'email' or 'telegram', and keeps the non-secret settings of its kind
-- (the mail server and addresses, the chat) as JSON in a new column. The SMTP
-- password and the bot token are not here: they live in the Nomad Variable
-- caravana/notifications/<id>.
--
-- SQLite can't change a CHECK constraint, so notification_channels is made
-- again, as 0022 and 0036 did for their tables. Unlike those, another table
-- references this one: notification_deliveries has a foreign key to it with ON
-- DELETE CASCADE, foreign keys are on, and a migration runs in one transaction
-- (where PRAGMA foreign_keys can't be changed). Dropping the old channels table
-- first would therefore delete every delivery. So the deliveries are made
-- again too, pointing at the new channels table, and only then are the old
-- tables dropped (nothing references them any more). The renames last make
-- SQLite rewrite the new deliveries table's reference to the final name.
CREATE TABLE notification_channels_new (
  id          TEXT PRIMARY KEY,                 -- "nch_" + 12 random hex
  kind        TEXT NOT NULL CHECK (kind IN ('webhook', 'slack', 'discord', 'ntfy', 'email', 'telegram')),
  name        TEXT NOT NULL,
  address     TEXT NOT NULL,                    -- what the console shows (masked for slack/discord)
  events      TEXT NOT NULL,                    -- JSON ["problems","deploy_failed","deploy_succeeded"]
  paused      INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  settings    TEXT NOT NULL DEFAULT '{}'        -- JSON of the kind's non-secret settings (email, telegram)
) STRICT;
INSERT INTO notification_channels_new (id, kind, name, address, events, paused, created_at, updated_at)
  SELECT id, kind, name, address, events, paused, created_at, updated_at FROM notification_channels;

CREATE TABLE notification_deliveries_new (
  id          TEXT PRIMARY KEY,                 -- "ndl_" + 12 random hex
  channel_id  TEXT NOT NULL REFERENCES notification_channels_new (id) ON DELETE CASCADE,
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
INSERT INTO notification_deliveries_new (id, channel_id, event_type, title, payload, status, attempts, next_at, error, created_at, finished_at)
  SELECT id, channel_id, event_type, title, payload, status, attempts, next_at, error, created_at, finished_at FROM notification_deliveries;

DROP TABLE notification_deliveries;
DROP TABLE notification_channels;
ALTER TABLE notification_channels_new RENAME TO notification_channels;
ALTER TABLE notification_deliveries_new RENAME TO notification_deliveries;

CREATE INDEX deliveries_due ON notification_deliveries (status, next_at);
CREATE INDEX deliveries_by_channel ON notification_deliveries (channel_id, created_at DESC);
