-- Feature 063: Caravana backs itself up, and is restored from that backup.
--
-- self_backup_settings is the owner's one row: whether the daily backup is on, the UTC hour, how many
-- backups to keep, and the PUBLIC half of the recovery key (base64 of the 32-byte X25519 public key) with
-- its fingerprint (first 16 hex digits of its SHA-256) and when the key was made. The private half is not
-- stored anywhere: the owner keeps it. With no key (public_key = '') nothing is backed up.
--
-- self_backups is one row per backup run. A row holds the object's name in the bucket, the destination's
-- name, sizes, the version and migration of the Caravana that made it, a warning (a retention delete
-- that failed) and one sentence of error in Caravana's own words. Never a key, a variable or a byte of
-- the backup. `trigger` is schedule (the daily clock), owner (Back up now) or key (made at once when a
-- new recovery key was made). `schema_version` is the newest migration of the database in the backup.
--
-- self_restores and self_restore_items record a restore that `caravana admin restore` made before
-- Caravana started: the restore (the backup it came from) and what Caravana then does about each app
-- (redeploy) and, when the owner asks, each service's data (data). These rows are inserted into the
-- restored database, so they live on the new server.
--
-- Times are RFC 3339 text in UTC with nine fraction digits, like every other table's; NULL is not
-- known yet.

CREATE TABLE self_backup_settings (
  id              INTEGER PRIMARY KEY CHECK (id = 1),
  enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  hour            INTEGER NOT NULL DEFAULT 3 CHECK (hour BETWEEN 0 AND 23),
  keep            INTEGER NOT NULL DEFAULT 14 CHECK (keep BETWEEN 1 AND 90),
  public_key      TEXT NOT NULL DEFAULT '',
  key_fingerprint TEXT NOT NULL DEFAULT '',
  key_created_at  TEXT
) STRICT;
INSERT INTO self_backup_settings (id) VALUES (1);

CREATE TABLE self_backups (
  id              TEXT PRIMARY KEY,           -- 32 lowercase hex characters
  status          TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  trigger         TEXT NOT NULL CHECK (trigger IN ('schedule', 'owner', 'key')),
  actor           TEXT NOT NULL DEFAULT '',   -- the person's email, '' for the clock
  started_at      TEXT NOT NULL,
  ended_at        TEXT,
  destination     TEXT NOT NULL DEFAULT '',   -- the destination's name
  object          TEXT NOT NULL DEFAULT '',   -- the key in the bucket, on success
  bytes           INTEGER NOT NULL DEFAULT 0,
  key_fingerprint TEXT NOT NULL DEFAULT '',
  version         TEXT NOT NULL DEFAULT '',   -- the Caravana that made it
  schema_version  INTEGER NOT NULL DEFAULT 0,
  warning         TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX self_backups_by_time ON self_backups (started_at, id);
CREATE UNIQUE INDEX self_backups_one_running ON self_backups ((1)) WHERE status = 'running';

CREATE TABLE self_restores (
  id                TEXT PRIMARY KEY,         -- 32 lowercase hex characters
  status            TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done')),
  object            TEXT NOT NULL DEFAULT '', -- the backup it came from
  backup_created_at TEXT NOT NULL,
  backup_version    TEXT NOT NULL DEFAULT '',
  restored_at       TEXT NOT NULL,            -- when `caravana admin restore` finished
  ended_at          TEXT
) STRICT;

CREATE TABLE self_restore_items (
  restore_id TEXT NOT NULL REFERENCES self_restores (id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('redeploy', 'data')),
  app        TEXT NOT NULL,
  service    TEXT NOT NULL DEFAULT '',        -- the service, for a data item
  status     TEXT NOT NULL CHECK (status IN ('waiting', 'running', 'done', 'failed', 'skipped')),
  ref        TEXT NOT NULL DEFAULT '',        -- the deployment, or the restore record, it made
  reason     TEXT NOT NULL DEFAULT '',        -- one sentence for failed and skipped
  started_at TEXT,
  ended_at   TEXT,
  PRIMARY KEY (restore_id, seq)
) STRICT;
