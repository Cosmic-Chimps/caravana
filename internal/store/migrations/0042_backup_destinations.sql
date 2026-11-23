-- Feature 052: several backup destinations and a second copy of each backup.
--
-- The installation's one destination (migration 0005) becomes the destination named "main", and the
-- default. Its secret key stays in the Nomad Variable it always had: secret_path says where each
-- destination's key is. No secret key is in this table.
CREATE TABLE backup_destinations (
  name          TEXT PRIMARY KEY,            -- 1 to 32 lowercase letters, digits and hyphens
  endpoint      TEXT NOT NULL,
  region        TEXT NOT NULL,
  bucket        TEXT NOT NULL,
  prefix        TEXT NOT NULL DEFAULT '',
  access_key_id TEXT NOT NULL,
  secret_path   TEXT NOT NULL,               -- the Nomad Variable that holds the secret key
  is_default    INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
  tested_at     TEXT,                        -- RFC 3339, UTC, nine fraction digits; NULL until a test ran
  test_ok       INTEGER CHECK (test_ok IN (0, 1)),
  test_message  TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL
) STRICT;

-- Exactly one destination is the default while any exists.
CREATE UNIQUE INDEX backup_destinations_one_default ON backup_destinations (is_default) WHERE is_default = 1;

INSERT INTO backup_destinations (name, endpoint, region, bucket, prefix, access_key_id, secret_path, is_default, updated_at)
SELECT 'main', endpoint, region, bucket, prefix, access_key_id, 'caravana/backup-destination', 1, updated_at
FROM backup_destination;

DROP TABLE backup_destination;

-- Where each backup went and each restore read from. Rows made before this migration went to the
-- one destination there was, now "main"; with none set they stay empty.
ALTER TABLE backups ADD COLUMN destination TEXT NOT NULL DEFAULT '';
ALTER TABLE restores ADD COLUMN destination TEXT NOT NULL DEFAULT '';
UPDATE backups SET destination = 'main' WHERE EXISTS (SELECT 1 FROM backup_destinations WHERE name = 'main');
UPDATE restores SET destination = 'main' WHERE EXISTS (SELECT 1 FROM backup_destinations WHERE name = 'main');

-- A project's own default destination; NULL means the installation's.
ALTER TABLE projects ADD COLUMN backup_destination TEXT;

-- One row per copy of a backup to another destination.
CREATE TABLE backup_copies (
  app_id      TEXT NOT NULL,
  backup_id   TEXT NOT NULL,
  destination TEXT NOT NULL,                 -- the destination copied to
  status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
  object      TEXT NOT NULL DEFAULT '',      -- the copy's path in that destination, on success
  bytes       INTEGER NOT NULL DEFAULT 0,
  message     TEXT NOT NULL DEFAULT '',      -- why it failed
  expired     INTEGER NOT NULL DEFAULT 0,    -- 1: deleted from that destination by retention
  job_id      TEXT NOT NULL DEFAULT '',      -- the copy's Nomad job while it runs
  created_at  TEXT NOT NULL,
  started_at  TEXT,
  finished_at TEXT,
  PRIMARY KEY (app_id, backup_id, destination),
  FOREIGN KEY (app_id, backup_id) REFERENCES backups (app_id, id) ON DELETE CASCADE
) STRICT;
CREATE INDEX backup_copies_by_status ON backup_copies (status);
