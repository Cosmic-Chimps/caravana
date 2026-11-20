-- Feature 046: MySQL and MongoDB dumps. A backup or restore may be made by the
-- 'mysql' or 'mongodb' engine, which dump the running database as 'postgres'
-- does.
--
-- SQLite can't change a CHECK constraint, so both tables are made again with
-- the two engines among the allowed ones, their rows copied and their indexes
-- put back, as 0022 and 0029 did. Every column, default and other CHECK is as
-- 0029 left it (backups: 0005, 0008, 0021 and 0029's pin columns; restores:
-- 0029's rebuild). No table references either one.
CREATE TABLE backups_new (
  app_id      TEXT NOT NULL REFERENCES apps (id),
  id          TEXT NOT NULL,              -- <service>-<unix seconds>
  service     TEXT NOT NULL,
  job_id      TEXT NOT NULL,              -- the run's Nomad job, or <app>_<service>_<volumes|sqlite>/<ts> for a run in the copy
  status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  object      TEXT NOT NULL DEFAULT '',   -- bucket path of the backup, on success
  bytes       INTEGER NOT NULL DEFAULT 0,
  message     TEXT NOT NULL DEFAULT '',   -- why it failed
  expired     INTEGER NOT NULL DEFAULT 0, -- 1: deleted from the bucket by retention
  started_at  TEXT NOT NULL,
  finished_at TEXT,                       -- NULL while running
  engine      TEXT NOT NULL DEFAULT 'postgres'
              CHECK (engine IN ('postgres', 'mysql', 'mongodb', 'volume', 'sqlite')),
  stopped_ms  INTEGER,                    -- volume runs: how long the service was stopped
  volumes     TEXT,                       -- volume backups: JSON [{name, bytes, sizeKB}]
  trigger     TEXT NOT NULL DEFAULT 'timer'
              CHECK (trigger IN ('timer', 'owner')),
  pin         TEXT,                       -- the label the backup is pinned under; NULL when it isn't
  pinned_at   TEXT,
  PRIMARY KEY (app_id, id)
) STRICT;

INSERT INTO backups_new (app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at,
                         engine, stopped_ms, volumes, trigger, pin, pinned_at)
  SELECT app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at,
         engine, stopped_ms, volumes, trigger, pin, pinned_at
  FROM backups;

DROP TABLE backups;
ALTER TABLE backups_new RENAME TO backups;
CREATE INDEX backups_by_time ON backups (app_id, started_at DESC);
CREATE UNIQUE INDEX backups_pin ON backups (app_id, service, pin) WHERE pin IS NOT NULL;

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
              CHECK (engine IN ('postgres', 'mysql', 'mongodb', 'volume', 'sqlite')),
  stopped_ms  INTEGER,                    -- volume and sqlite restores: how long the service was stopped
  trigger     TEXT NOT NULL DEFAULT 'owner'
              CHECK (trigger IN ('owner', 'schedule')),
  pin         TEXT                        -- the label a scheduled run asked for; NULL for the owner's
) STRICT;

INSERT INTO restores_new (id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms, trigger, pin)
  SELECT id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms, trigger, pin
  FROM restores;

DROP TABLE restores;
ALTER TABLE restores_new RENAME TO restores;
CREATE INDEX restores_by_time ON restores (app_id, created_at DESC);
