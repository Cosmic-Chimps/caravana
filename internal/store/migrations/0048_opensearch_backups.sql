-- caravana: foreign_keys off
-- Feature 061: OpenSearch snapshots. A backup or restore may be made by the
-- 'opensearch' engine, which has the cluster snapshot itself while it runs.
--
-- SQLite can't change a CHECK constraint, so both tables are made again with
-- the engine among the allowed ones, their rows copied and their indexes put
-- back, as 0022 and 0036 did. Every column, default and other CHECK is as 0043
-- left it (backups: 0036's, 0042's destination and 0043's actor; restores the
-- same).
--
-- Unlike those two, this one runs with foreign keys enforced (054), and
-- backup_copies references backups with ON DELETE CASCADE: dropping the old
-- backups would delete every copy's row, and a row whose parent is gone (kept
-- from before enforcement, see TestOrphansFromBeforeForeignKeysAreKept) could
-- not be copied at all. So the runner turns enforcement off for this
-- migration (the first line, as SQLite's own guide does for a table rebuild),
-- and backup_copies keeps its reference by name: the renamed table is the one
-- it points at.
CREATE TABLE backups_new (
  app_id      TEXT NOT NULL REFERENCES apps (id),
  id          TEXT NOT NULL,              -- <service>-<unix seconds>
  service     TEXT NOT NULL,
  job_id      TEXT NOT NULL,              -- the run's Nomad job, or <app>_<service>_<volumes|sqlite|opensearch>/<ts> for a run in the copy
  status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  object      TEXT NOT NULL DEFAULT '',   -- bucket path of the backup, on success
  bytes       INTEGER NOT NULL DEFAULT 0,
  message     TEXT NOT NULL DEFAULT '',   -- why it failed
  expired     INTEGER NOT NULL DEFAULT 0, -- 1: deleted from the bucket by retention
  started_at  TEXT NOT NULL,
  finished_at TEXT,                       -- NULL while running
  engine      TEXT NOT NULL DEFAULT 'postgres'
              CHECK (engine IN ('postgres', 'mysql', 'mongodb', 'volume', 'sqlite', 'opensearch')),
  stopped_ms  INTEGER,                    -- volume runs: how long the service was stopped
  volumes     TEXT,                       -- volume backups: JSON [{name, bytes, sizeKB}]
  trigger     TEXT NOT NULL DEFAULT 'timer'
              CHECK (trigger IN ('timer', 'owner')),
  pin         TEXT,                       -- the label the backup is pinned under; NULL when it isn't
  pinned_at   TEXT,
  destination TEXT NOT NULL DEFAULT '',
  actor       TEXT,
  PRIMARY KEY (app_id, id)
) STRICT;

INSERT INTO backups_new (app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at,
                         engine, stopped_ms, volumes, trigger, pin, pinned_at, destination, actor)
  SELECT app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at,
         engine, stopped_ms, volumes, trigger, pin, pinned_at, destination, actor
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
              CHECK (engine IN ('postgres', 'mysql', 'mongodb', 'volume', 'sqlite', 'opensearch')),
  stopped_ms  INTEGER,                    -- volume and sqlite restores: how long the service was stopped
  trigger     TEXT NOT NULL DEFAULT 'owner'
              CHECK (trigger IN ('owner', 'schedule')),
  pin         TEXT,                       -- the label a scheduled run asked for; NULL for the owner's
  destination TEXT NOT NULL DEFAULT '',
  actor       TEXT
) STRICT;

INSERT INTO restores_new (id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms, trigger, pin, destination, actor)
  SELECT id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms, trigger, pin, destination, actor
  FROM restores;

DROP TABLE restores;
ALTER TABLE restores_new RENAME TO restores;
CREATE INDEX restores_by_time ON restores (app_id, created_at DESC);
