-- Feature 044: image scanning.
--
-- addons: one row per add-on the owner installed or tried to install (today only
-- 'image-scanner'). No row means "available".
--
-- image_scans: one row per image digest (repo@sha256:...): the newest successful result
-- (counts, when, which scanner and database) and the newest try, which may have failed or
-- been skipped without touching the findings. image_findings holds the findings of that
-- result; a finding keeps its first_seen across scans and has its last_seen moved by each.
-- A finding is the scanner's database and the package's name and version, never anything an
-- app wrote.
--
-- scan_runs: the queue and the history. A run is queued, running, then ok, failed or
-- skipped; image is '' for the install's database run. Only the newest 200 are kept.
--
-- scan_settings: the owner's settings, one row.

CREATE TABLE addons (
  name         TEXT PRIMARY KEY,
  state        TEXT NOT NULL CHECK (state IN ('installing', 'installed', 'failed', 'removing')),
  version      TEXT NOT NULL,
  installed_at TEXT NOT NULL DEFAULT '',
  updated_at   TEXT NOT NULL,
  reason       TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE image_scans (
  image           TEXT PRIMARY KEY,
  scanned_at      TEXT NOT NULL DEFAULT '',
  db_built        TEXT NOT NULL DEFAULT '',
  db_schema       TEXT NOT NULL DEFAULT '',
  scanner         TEXT NOT NULL DEFAULT '',
  cut             INTEGER NOT NULL DEFAULT 0,
  sbom_kept       INTEGER NOT NULL DEFAULT 0,
  critical        INTEGER NOT NULL DEFAULT 0,
  high            INTEGER NOT NULL DEFAULT 0,
  medium          INTEGER NOT NULL DEFAULT 0,
  low             INTEGER NOT NULL DEFAULT 0,
  info            INTEGER NOT NULL DEFAULT 0,
  fixable_critical INTEGER NOT NULL DEFAULT 0,
  fixable_high    INTEGER NOT NULL DEFAULT 0,
  kev             INTEGER NOT NULL DEFAULT 0,
  tried_at        TEXT NOT NULL,
  status          TEXT NOT NULL CHECK (status IN ('ok', 'failed', 'skipped')),
  reason          TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE image_findings (
  id              TEXT NOT NULL,
  image           TEXT NOT NULL REFERENCES image_scans (image) ON DELETE CASCADE,
  advisory        TEXT NOT NULL,
  cve             TEXT NOT NULL,
  package         TEXT NOT NULL,
  version         TEXT NOT NULL,
  type            TEXT NOT NULL,
  severity        TEXT NOT NULL CHECK (severity IN ('critical', 'high', 'medium', 'low', 'info')),
  fix_state       TEXT NOT NULL,
  fixed_in        TEXT NOT NULL,            -- the fixed versions joined by a newline
  url             TEXT NOT NULL,
  epss            REAL,
  epss_percentile REAL,
  kev             INTEGER NOT NULL DEFAULT 0,
  first_seen      TEXT NOT NULL,
  last_seen       TEXT NOT NULL,
  PRIMARY KEY (image, id)
) STRICT;

CREATE TABLE scan_runs (
  id         TEXT PRIMARY KEY,
  image      TEXT NOT NULL,                 -- '' for the install's database run
  trigger    TEXT NOT NULL CHECK (trigger IN ('install', 'deploy', 'daily', 'owner', 'gate')),
  source     TEXT NOT NULL CHECK (source IN ('registry', 'sbom', 'database')),
  status     TEXT NOT NULL CHECK (status IN ('queued', 'running', 'ok', 'failed', 'skipped')),
  reason     TEXT NOT NULL DEFAULT '',
  scanner    TEXT NOT NULL DEFAULT '',
  db_built   TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  ended_at   TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX scan_runs_status ON scan_runs (status, created_at);
CREATE INDEX scan_runs_image ON scan_runs (image, created_at);

CREATE TABLE scan_settings (
  id                      INTEGER PRIMARY KEY CHECK (id = 1),
  notify_level            TEXT NOT NULL DEFAULT 'critical' CHECK (notify_level IN ('critical', 'high')),
  refuse_fixable_critical INTEGER NOT NULL DEFAULT 0,
  daily_hour              INTEGER NOT NULL DEFAULT 4 CHECK (daily_hour BETWEEN 0 AND 23),
  database_url            TEXT NOT NULL DEFAULT '',
  last_daily              TEXT NOT NULL DEFAULT ''
) STRICT;
INSERT INTO scan_settings (id) VALUES (1);
