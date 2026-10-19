-- Feature 020.6: a terminal into a copy is one more action in the record.
--
-- SQLite can't change a CHECK constraint, so the table is made again with
-- 'terminal' among the actions, the rows are copied and the index is put back.
-- A terminal's record holds the shell asked for (program), the exit code of the
-- shell and a fixed end reason (error), never what was typed or printed.
CREATE TABLE app_actions_new (
  id          TEXT PRIMARY KEY,                 -- "act_" + 12 random hex
  app_id      TEXT NOT NULL,
  action      TEXT NOT NULL CHECK (action IN ('stop', 'start', 'restart', 'run', 'terminal')),
  trigger     TEXT NOT NULL,                    -- console | manual
  status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  copy_id     TEXT NOT NULL DEFAULT '',
  service     TEXT NOT NULL DEFAULT '',
  program     TEXT NOT NULL DEFAULT '',         -- run: the command's first word; terminal: auto, bash or sh
  exit_code   INTEGER,                          -- run, terminal: NULL when it timed out or didn't end with one
  error       TEXT NOT NULL DEFAULT '',         -- never command output, input or screen
  started_at  TEXT NOT NULL,
  finished_at TEXT
) STRICT;

INSERT INTO app_actions_new (id, app_id, action, trigger, status, copy_id, service, program, exit_code, error, started_at, finished_at)
  SELECT id, app_id, action, trigger, status, copy_id, service, program, exit_code, error, started_at, finished_at
  FROM app_actions;

DROP TABLE app_actions;
ALTER TABLE app_actions_new RENAME TO app_actions;
CREATE INDEX app_actions_by_app ON app_actions (app_id, started_at);
