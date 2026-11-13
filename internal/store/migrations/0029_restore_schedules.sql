-- Feature 036: pin a backup under a label, and restore it on a schedule.
--
-- A pin is a label on a backup row. Retention (every engine's) never deletes a
-- pinned backup and counts only the unpinned ones against keep. A label is
-- unique per service, so pinning a label moves it.
ALTER TABLE backups ADD COLUMN pin TEXT;
ALTER TABLE backups ADD COLUMN pinned_at TEXT;
CREATE UNIQUE INDEX backups_pin ON backups (app_id, service, pin) WHERE pin IS NOT NULL;

-- A restore may now be 'skipped' (a scheduled slot that came while a backup or
-- restore of the service was running), may be started by the schedule, and
-- records the label a scheduled run asked for. SQLite can't change a CHECK
-- constraint, so the table is made again, its rows copied (every old restore
-- was the owner's, which the column's default says) and its index put back, as
-- 0022 did. No table references it.
CREATE TABLE restores_new (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL REFERENCES apps (id),
  service     TEXT NOT NULL,
  backup_id   TEXT NOT NULL,              -- '' for a scheduled run refused before it found a backup
  status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'skipped')),
  message     TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  finished_at TEXT,
  engine      TEXT NOT NULL DEFAULT 'postgres'
              CHECK (engine IN ('postgres', 'volume', 'sqlite')),
  stopped_ms  INTEGER,                    -- volume and sqlite restores: how long the service was stopped
  trigger     TEXT NOT NULL DEFAULT 'owner'
              CHECK (trigger IN ('owner', 'schedule')),
  pin         TEXT                        -- the label a scheduled run asked for; NULL for the owner's
) STRICT;

INSERT INTO restores_new (id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms)
  SELECT id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms
  FROM restores;

DROP TABLE restores;
ALTER TABLE restores_new RENAME TO restores;
CREATE INDEX restores_by_time ON restores (app_id, created_at DESC);
