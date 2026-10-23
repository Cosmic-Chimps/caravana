-- Feature 022: schedules, a command on a timer in a fresh copy of an app, and
-- the record of each run.

CREATE TABLE schedules (
  id              TEXT PRIMARY KEY,                       -- 12 lowercase hex
  app_id          TEXT NOT NULL REFERENCES apps (id),
  service         TEXT NOT NULL,                          -- the service whose image, env and secrets a run uses
  name            TEXT NOT NULL,
  command         TEXT NOT NULL,                          -- JSON array of strings, program first
  cron            TEXT NOT NULL,                          -- five fields, UTC
  stop_after_s    INTEGER NOT NULL,
  memory_mb       INTEGER NOT NULL,
  skip_if_running INTEGER NOT NULL DEFAULT 1,             -- 1: skip the slot while a run goes; 0: allow overlap
  paused_at       TEXT,                                   -- NULL when not paused
  armed_at        TEXT NOT NULL,                          -- the clock's base: create, resume, a cron change
  created_at      TEXT NOT NULL,                          -- RFC 3339, UTC
  updated_at      TEXT NOT NULL,
  UNIQUE (app_id, name)
) STRICT;

-- One row per run, written before the job is registered. stdout and stderr are
-- the last 64 KiB of each, with the service's secret values masked; NULL when
-- not kept.
CREATE TABLE schedule_runs (
  id                TEXT PRIMARY KEY,                     -- 12 hex; the Nomad job is <app>_run-<id>
  schedule_id       TEXT NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
  app_id            TEXT NOT NULL,
  service           TEXT NOT NULL,
  trigger           TEXT NOT NULL,                        -- timer | owner
  slot_at           TEXT,                                 -- the cron instant this run answers; NULL for an owner run
  command           TEXT NOT NULL,                        -- JSON, as run
  deployment_id     TEXT,                                 -- the healthy deployment whose digest ran
  job_version       INTEGER,
  status            TEXT NOT NULL CHECK (status IN
    ('starting', 'running', 'ok', 'failed', 'stopped_at_limit', 'stopped', 'skipped', 'could_not_start')),
  exit_code         INTEGER,
  error             TEXT,                                 -- Caravana's words, never the output
  stop_reason       TEXT,                                 -- owner | limit | app_stopped | schedule_removed
  stop_requested_at TEXT,
  started_at        TEXT NOT NULL,
  task_started_at   TEXT,
  finished_at       TEXT,
  stdout            TEXT,
  stderr            TEXT,
  stdout_cut        INTEGER NOT NULL DEFAULT 0,
  stderr_cut        INTEGER NOT NULL DEFAULT 0,
  output_kept       INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX schedule_runs_by_schedule ON schedule_runs (schedule_id, started_at);
CREATE INDEX schedule_runs_by_app ON schedule_runs (app_id);
CREATE INDEX schedule_runs_by_status ON schedule_runs (status);
