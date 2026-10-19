-- Feature 020.5: stopped apps and the record of app actions.

-- When the app was stopped; NULL means it is not stopped. Kept beside the
-- document, not in it, so no stored revision changes.
ALTER TABLE apps ADD COLUMN stopped_at TEXT;

-- One row per action the owner asked for. Nothing here is command output: a
-- run keeps its first word (program) and exit code only, and error never
-- holds what the command printed.
CREATE TABLE app_actions (
  id          TEXT PRIMARY KEY,                 -- "act_" + 12 random hex
  app_id      TEXT NOT NULL,
  action      TEXT NOT NULL CHECK (action IN ('stop', 'start', 'restart', 'run')),
  trigger     TEXT NOT NULL,                    -- console | manual
  status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  copy_id     TEXT NOT NULL DEFAULT '',
  service     TEXT NOT NULL DEFAULT '',
  program     TEXT NOT NULL DEFAULT '',         -- run: the command's first word, nothing else
  exit_code   INTEGER,                          -- run: NULL when it timed out or didn't start
  error       TEXT NOT NULL DEFAULT '',         -- never command output
  started_at  TEXT NOT NULL,
  finished_at TEXT
) STRICT;
CREATE INDEX app_actions_by_app ON app_actions (app_id, started_at);
