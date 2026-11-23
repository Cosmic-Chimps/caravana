-- Feature 050: building a service's image from a Git repository.
--
-- builds: one row per deployment of a built service, written when the deployment is
-- made (queued) and settled when the build ends. The row is the deployment's Build
-- stage: what was built (repository, branch, the commit's sha, author and subject),
-- where it went (image, digest), why it failed (error, a sentence in Caravana's words
-- plus the last log lines) and the build's log (its last 64 KiB, with secrets masked;
-- log_cut says it was longer). The log is kept for the newest 50 builds of an app; an
-- older row keeps every other field and has log NULL. A deploy key's private half, a
-- token and the push credentials are never here: they live in Nomad Variables.
--
-- build_settings: the owner's limits for every build, one row.

CREATE TABLE builds (
  id            TEXT PRIMARY KEY,
  deployment_id TEXT NOT NULL UNIQUE REFERENCES deployments (id) ON DELETE CASCADE,
  app_id        TEXT NOT NULL,
  service       TEXT NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  repository    TEXT NOT NULL,
  branch        TEXT NOT NULL,
  commit_sha    TEXT NOT NULL DEFAULT '',
  author        TEXT NOT NULL DEFAULT '',
  subject       TEXT NOT NULL DEFAULT '',
  image         TEXT NOT NULL DEFAULT '',
  digest        TEXT NOT NULL DEFAULT '',
  error         TEXT NOT NULL DEFAULT '',
  log           TEXT,
  log_cut       INTEGER NOT NULL DEFAULT 0,
  node          TEXT NOT NULL DEFAULT '',
  queued_at     TEXT NOT NULL,
  started_at    TEXT NOT NULL DEFAULT '',
  ended_at      TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX builds_app ON builds (app_id, queued_at);
CREATE INDEX builds_status ON builds (status);

CREATE TABLE build_settings (
  id              INTEGER PRIMARY KEY CHECK (id = 1),
  memory_mb       INTEGER NOT NULL DEFAULT 2048  CHECK (memory_mb BETWEEN 512 AND 16384),
  cpu_mhz         INTEGER NOT NULL DEFAULT 2000  CHECK (cpu_mhz BETWEEN 500 AND 32000),
  timeout_minutes INTEGER NOT NULL DEFAULT 30    CHECK (timeout_minutes BETWEEN 5 AND 180),
  concurrency     INTEGER NOT NULL DEFAULT 1     CHECK (concurrency BETWEEN 1 AND 4),
  cache_mb        INTEGER NOT NULL DEFAULT 5120  CHECK (cache_mb BETWEEN 0 AND 102400)
) STRICT;
INSERT INTO build_settings (id) VALUES (1);
