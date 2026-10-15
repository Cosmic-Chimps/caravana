-- Feature 016: volume backups and the volumes panel.

-- Which engine made a backup or a restore. Every row written before this
-- feature is a Postgres one, which is what the default says.
ALTER TABLE backups  ADD COLUMN engine     TEXT NOT NULL DEFAULT 'postgres'
                     CHECK (engine IN ('postgres', 'volume'));
-- Volume runs: how long the service was stopped, in milliseconds. NULL when
-- not measured, and for Postgres runs, which never stop the service.
ALTER TABLE backups  ADD COLUMN stopped_ms INTEGER;
-- Volume backups: JSON [{name, bytes, sizeKB}], one entry per archived volume.
-- NULL for Postgres backups.
ALTER TABLE backups  ADD COLUMN volumes    TEXT;
ALTER TABLE restores ADD COLUMN engine     TEXT NOT NULL DEFAULT 'postgres'
                     CHECK (engine IN ('postgres', 'volume'));
ALTER TABLE restores ADD COLUMN stopped_ms INTEGER;

-- The last measured size of each volume of an app, read from the copy's
-- caravana-volumes task. Kept so the panel can answer when no copy runs.
CREATE TABLE volume_sizes (
  app_id      TEXT NOT NULL REFERENCES apps (id),
  volume      TEXT NOT NULL,
  kilobytes   INTEGER NOT NULL,
  measured_at TEXT NOT NULL,   -- RFC 3339, UTC
  PRIMARY KEY (app_id, volume)
) STRICT;
