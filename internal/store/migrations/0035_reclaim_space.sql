-- Feature 045: reclaiming space on a server.
--
-- cleanup_settings is the owner's one row: whether every ready server is cleaned daily (04:00 UTC),
-- how many healthy deployments of each app keep their images (a rollback never waits on a pull), and
-- the UTC date the daily run last requested, so a restart after 04:00 doesn't request twice.
--
-- image_cleanups is one row per clean-up of a server's unused images: requested by the owner or the
-- daily setting, handed to the server's agent in its sync answer, settled from the agent's next report.
-- It holds counts and one sentence in Caravana's words, never an image name or a Docker message beyond
-- the agent's first error cut to 200 characters. At most one is open (requested or running) per
-- server; a clean-up is deleted with its server, as a move is.
--
-- volume_deletions is one row per deletion of an orphan Nomad host volume, a volume no stored app
-- declares: with the archive to the backup destination first or without. The app is only a name read
-- from the volume's (the app may be deleted since), so there is no foreign key to it, and the row
-- stays as history when the volume is gone.
--
-- Times are RFC 3339 text in UTC with nine fraction digits, so they sort as text, like every other
-- table's; a NULL finished_at (or started_at) is not known yet.

CREATE TABLE cleanup_settings (
  id                  INTEGER PRIMARY KEY CHECK (id = 1),
  automatic           INTEGER NOT NULL DEFAULT 1,
  keep_deployments    INTEGER NOT NULL DEFAULT 3 CHECK (keep_deployments BETWEEN 1 AND 10),
  last_automatic_day  TEXT NOT NULL DEFAULT ''             -- YYYY-MM-DD in UTC, '' before the first daily request
) STRICT;
INSERT INTO cleanup_settings (id) VALUES (1);

CREATE TABLE image_cleanups (
  id               TEXT PRIMARY KEY,                       -- 32 lowercase hex characters: the agent's request ID
  server_idx       INTEGER NOT NULL REFERENCES servers (idx) ON DELETE CASCADE,
  server_name      TEXT NOT NULL,                          -- the name at the time: the feed's words
  trigger          TEXT NOT NULL CHECK (trigger IN ('owner', 'schedule')),
  docker_volumes   INTEGER NOT NULL DEFAULT 0,             -- Docker's leftover volumes go too
  requested_at     TEXT NOT NULL,
  started_at       TEXT,                                   -- when the agent was handed it
  finished_at      TEXT,
  status           TEXT NOT NULL CHECK (status IN ('requested', 'running', 'done', 'failed', 'lost')),
  images_removed   INTEGER NOT NULL DEFAULT 0,
  bytes_freed      INTEGER NOT NULL DEFAULT 0,
  volumes_removed  INTEGER NOT NULL DEFAULT 0,
  error            TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX image_cleanups_by_server ON image_cleanups (server_idx, requested_at);
CREATE UNIQUE INDEX image_cleanups_one_open ON image_cleanups (server_idx) WHERE status IN ('requested', 'running');

CREATE TABLE volume_deletions (
  id              TEXT PRIMARY KEY,                        -- "vd_" + 26 lowercase base32
  volume          TEXT NOT NULL,                           -- Nomad's name, <app>_<volume>
  app_id          TEXT NOT NULL DEFAULT '',                -- the app the name reads as; it may not exist any more
  server_name     TEXT NOT NULL DEFAULT '',
  back_up_first   INTEGER NOT NULL DEFAULT 0,
  trigger         TEXT NOT NULL DEFAULT 'owner',
  requested_at    TEXT NOT NULL,
  finished_at     TEXT,
  status          TEXT NOT NULL CHECK (status IN ('archiving', 'deleting', 'done', 'failed')),
  archive_object  TEXT NOT NULL DEFAULT '',                -- where the archive is in the backup destination
  bytes           INTEGER NOT NULL DEFAULT 0,              -- the size last measured, 0 when unknown
  error           TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX volume_deletions_by_volume ON volume_deletions (volume, requested_at);
