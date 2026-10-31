-- Feature 027: the Operator view. Four small tables, none of them app data an
-- owner edits.
--
-- Times are RFC 3339 text in UTC with nine fraction digits, so they sort as
-- text, like every other table's. Nothing here holds a secret value, an env
-- value or command output.

-- What the Updates page knows about Caravana's own releases. One row, made
-- here with the owner's setting on; the check fills the rest. A column the
-- check hasn't filled is empty text.
CREATE TABLE release_check (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  enabled      INTEGER NOT NULL DEFAULT 1,      -- the owner's setting: 0 makes no request at all
  latest_tag   TEXT NOT NULL DEFAULT '',        -- v0.16.0
  published_at TEXT NOT NULL DEFAULT '',        -- when the release was published
  url          TEXT NOT NULL DEFAULT '',        -- the release page
  body         TEXT NOT NULL DEFAULT '',        -- the release notes, capped at 16 KiB by the writer
  nomad_pin    TEXT NOT NULL DEFAULT '',        -- from the release's installer, when it was read
  traefik_pin  TEXT NOT NULL DEFAULT '',
  etag         TEXT NOT NULL DEFAULT '',        -- for If-None-Match
  checked_at   TEXT NOT NULL DEFAULT '',        -- the last attempt, good or not
  checked_ok_at TEXT NOT NULL DEFAULT '',       -- the last attempt that GitHub answered well
  error        TEXT NOT NULL DEFAULT '',        -- the last failure in Caravana's words; empty when it worked
  notified_tag TEXT NOT NULL DEFAULT ''         -- the last tag a caravana.release message was queued for
) STRICT;
INSERT INTO release_check (id) VALUES (1);

-- Things that happened to the host or to Caravana, in order: a boot, a new
-- Caravana version, a run of the OS's package manager. The key is what stops
-- the same thing being recorded twice (boot|<boot time>, version|<version>,
-- os|<apt run start>); NULL keys don't dedup. Pruned to the newest 200.
CREATE TABLE host_events (
  id     INTEGER PRIMARY KEY,
  kind   TEXT NOT NULL CHECK (kind IN ('boot', 'version', 'os_packages')),
  at     TEXT NOT NULL,                         -- the event's own time
  detail TEXT NOT NULL,                         -- JSON, e.g. {"from":"0.14.0","to":"0.15.0"}
  key    TEXT UNIQUE
) STRICT;
CREATE INDEX host_events_by_time ON host_events (at, id);

-- Every time the needs-you list got a problem, made one worse or lost one.
-- sent_problems forgets a problem when it is resolved; this keeps the history
-- for the activity feed. Written with sent_problems, in the same transaction,
-- pruned to the newest 200, and an app's rows go with the app.
CREATE TABLE problem_events (
  id      INTEGER PRIMARY KEY,
  key     TEXT NOT NULL,                        -- the sent_problems key
  kind    TEXT NOT NULL,                        -- the attention kind
  app_id  TEXT NOT NULL DEFAULT '',
  change  TEXT NOT NULL CHECK (change IN ('opened', 'worsened', 'resolved')),
  at      TEXT NOT NULL,
  summary TEXT NOT NULL,                        -- the title that was sent
  -- The needs-you list's own sentence for the problem when the event was written, as JSON
  -- [{"text":"...","mono":true}]: the names (app, service, schedule, mount, domain) are the mono
  -- parts. Empty for an event written before the column existed: the feed falls back to summary.
  sentence TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX problem_events_by_time ON problem_events (at, id);
CREATE INDEX problem_events_by_app ON problem_events (app_id);

-- The same sentence for a problem that is still open, so that the event that resolves it can say
-- what was resolved without reading the needs-you list (the item is gone by then).
ALTER TABLE sent_problems ADD COLUMN sentence TEXT NOT NULL DEFAULT '';

-- Small facts about the host that must survive a restart, such as when the
-- count of pending security updates first went above zero.
CREATE TABLE host_marks (
  key TEXT PRIMARY KEY,                         -- security_since
  at  TEXT NOT NULL
) STRICT;
