-- Feature 010: database backups.

-- The installation's one backup destination. Its secret key isn't here: it
-- lives in the Nomad Variable caravana/backup-destination.
CREATE TABLE backup_destination (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  endpoint      TEXT NOT NULL,
  region        TEXT NOT NULL,
  bucket        TEXT NOT NULL,
  prefix        TEXT NOT NULL DEFAULT '',
  access_key_id TEXT NOT NULL,
  updated_at    TEXT NOT NULL   -- RFC 3339, UTC
) STRICT;

-- One row per backup run, copied from Nomad's record of the run before
-- Nomad forgets it.
CREATE TABLE backups (
  app_id      TEXT NOT NULL REFERENCES apps (id),
  id          TEXT NOT NULL,              -- <service>-<unix seconds>
  service     TEXT NOT NULL,
  job_id      TEXT NOT NULL,              -- the run's Nomad job, <app>_<service>_backup/periodic-<ts>
  status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  object      TEXT NOT NULL DEFAULT '',   -- bucket path of the dump, on success
  bytes       INTEGER NOT NULL DEFAULT 0,
  message     TEXT NOT NULL DEFAULT '',   -- why it failed
  expired     INTEGER NOT NULL DEFAULT 0, -- 1: deleted from the bucket by retention
  started_at  TEXT NOT NULL,
  finished_at TEXT,                       -- NULL while running
  PRIMARY KEY (app_id, id)
) STRICT;
CREATE INDEX backups_by_time ON backups (app_id, started_at DESC);

CREATE TABLE restores (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL REFERENCES apps (id),
  service     TEXT NOT NULL,
  backup_id   TEXT NOT NULL,
  status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  message     TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  finished_at TEXT
) STRICT;
CREATE INDEX restores_by_time ON restores (app_id, created_at DESC);
