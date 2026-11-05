-- Feature 029: SQLite backups. A backup or restore may be made by the sqlite
-- engine.
--
-- SQLite can't change a CHECK constraint, so both tables are made again with
-- 'sqlite' among the engines, their rows copied and their indexes put back,
-- as 0014 did for app_actions. Every column, default and other CHECK is as
-- 0005, 0008 and 0021 left it. No table references either one.
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
              CHECK (engine IN ('postgres', 'volume', 'sqlite')),
  stopped_ms  INTEGER,                    -- volume runs: how long the service was stopped
  volumes     TEXT,                       -- volume backups: JSON [{name, bytes, sizeKB}]
  trigger     TEXT NOT NULL DEFAULT 'timer'
              CHECK (trigger IN ('timer', 'owner')),
  PRIMARY KEY (app_id, id)
) STRICT;

INSERT INTO backups_new (app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at,
                         engine, stopped_ms, volumes, trigger)
  SELECT app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at,
         engine, stopped_ms, volumes, trigger
  FROM backups;

DROP TABLE backups;
ALTER TABLE backups_new RENAME TO backups;
CREATE INDEX backups_by_time ON backups (app_id, started_at DESC);

CREATE TABLE restores_new (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL REFERENCES apps (id),
  service     TEXT NOT NULL,
  backup_id   TEXT NOT NULL,
  status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  message     TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  finished_at TEXT,
  engine      TEXT NOT NULL DEFAULT 'postgres'
              CHECK (engine IN ('postgres', 'volume', 'sqlite')),
  stopped_ms  INTEGER                     -- volume and sqlite restores: how long the service was stopped
) STRICT;

INSERT INTO restores_new (id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms)
  SELECT id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms
  FROM restores;

DROP TABLE restores;
ALTER TABLE restores_new RENAME TO restores;
CREATE INDEX restores_by_time ON restores (app_id, created_at DESC);
