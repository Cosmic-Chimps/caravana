-- Feature 043: autoscaling. A scale step is one more action in the record, with
-- the counts it went between, the reason in Caravana's words (numbers only) and
-- the two job versions that let the console read a scale's version as the
-- deployment's: job_version is the version Nomad made for the step,
-- base_version the version of the deployment it scaled.
--
-- SQLite can't change a CHECK constraint, so app_actions is made again, as 0014
-- did, with 'scale' among the actions and the five new columns; every row is
-- copied and the index is put back. No table references it.
CREATE TABLE app_actions_new (
  id           TEXT PRIMARY KEY,                 -- "act_" + 12 random hex
  app_id       TEXT NOT NULL,
  action       TEXT NOT NULL CHECK (action IN ('stop', 'start', 'restart', 'run', 'terminal', 'scale')),
  trigger      TEXT NOT NULL,                    -- console | manual | schedule | autoscale
  status       TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  copy_id      TEXT NOT NULL DEFAULT '',
  service      TEXT NOT NULL DEFAULT '',
  program      TEXT NOT NULL DEFAULT '',         -- run: the command's first word; terminal: auto, bash or sh
  exit_code    INTEGER,                          -- run, terminal: NULL when it timed out or didn't end with one
  error        TEXT NOT NULL DEFAULT '',         -- never command output, input or screen
  started_at   TEXT NOT NULL,
  finished_at  TEXT,
  from_count   INTEGER,                          -- scale: the count before
  to_count     INTEGER,                          -- scale: the count asked for
  reason       TEXT NOT NULL DEFAULT '',         -- scale: why, numbers only
  job_version  INTEGER,                          -- scale: the Nomad job version the step made; NULL when it failed or can't be told
  base_version INTEGER                           -- scale: the version of the deployment it scaled
) STRICT;

INSERT INTO app_actions_new (id, app_id, action, trigger, status, copy_id, service, program, exit_code, error, started_at, finished_at)
  SELECT id, app_id, action, trigger, status, copy_id, service, program, exit_code, error, started_at, finished_at
  FROM app_actions;

DROP TABLE app_actions;
ALTER TABLE app_actions_new RENAME TO app_actions;
CREATE INDEX app_actions_by_app ON app_actions (app_id, started_at);
-- Every read of copies maps their versions through the scale steps
-- (store.ScaleVersions): only those rows, without a scan of every action.
CREATE INDEX app_actions_scale_versions ON app_actions (app_id, job_version, base_version)
  WHERE action = 'scale' AND job_version IS NOT NULL AND base_version IS NOT NULL;

-- The count a scaled service has now, as the autoscaler last read it from Nomad
-- and as the last deploy registered it. The status counts these copies, not
-- Replicas, which is only the starting count.
CREATE TABLE service_counts (
  app_id     TEXT NOT NULL,
  service    TEXT NOT NULL,
  count      INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (app_id, service)
) STRICT;
