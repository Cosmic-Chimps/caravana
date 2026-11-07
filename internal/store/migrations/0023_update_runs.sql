-- Feature 030: updating Caravana from the console. One row per update the
-- owner (or the automatic setting) asked for, and the owner's automatic-update
-- setting beside the release check's.
--
-- Times are RFC 3339 text in UTC with nine fraction digits, so they sort as
-- text, like every other table's. A row holds a version, who asked and how it
-- went in one line of the updater's words, never the installer's log: the log
-- stays on the server, readable by root only, and `log` is only its path.

CREATE TABLE update_runs (
  id           TEXT PRIMARY KEY,                -- 32 lowercase hex characters, the request file's ID
  version      TEXT NOT NULL,                   -- 0.17.0, without a leading v
  from_version TEXT NOT NULL DEFAULT '',        -- the version that ran when the request was made
  trigger      TEXT NOT NULL CHECK (trigger IN ('console', 'api', 'automatic')),
  token_name   TEXT NOT NULL DEFAULT '',        -- the API token's name, for trigger 'api'
  requested_at TEXT NOT NULL,
  started_at   TEXT,                            -- when the updater started it; NULL until known
  finished_at  TEXT,                            -- NULL until the run is final
  state        TEXT NOT NULL CHECK (state IN
               ('requested', 'running', 'updated', 'rolled_back', 'failed', 'refused', 'lost')),
  reason       TEXT NOT NULL DEFAULT '',        -- one line, at most 300 characters; empty unless it failed
  log          TEXT NOT NULL DEFAULT ''         -- where the updater keeps the installer's output on the server
) STRICT;
CREATE INDEX update_runs_by_time ON update_runs (requested_at, id);

-- Automatic updates: off until the owner turns them on; the hour is UTC.
ALTER TABLE release_check ADD COLUMN auto_update INTEGER NOT NULL DEFAULT 0;
ALTER TABLE release_check ADD COLUMN auto_update_hour INTEGER NOT NULL DEFAULT 3
                          CHECK (auto_update_hour BETWEEN 0 AND 23);
