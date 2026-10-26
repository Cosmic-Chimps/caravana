-- Feature 025: monitoring. The ring buffer the sampler writes once a minute:
-- one row per app service for requests, errors, response-time buckets, CPU,
-- memory, copies and restarts, one row for the host and one per watched
-- filesystem. A source that wasn't read leaves its columns NULL (a gap, never a
-- zero). Minute rows (res 60) are kept 48 hours; each 10-minute slot is folded
-- into a res 600 row as it ends and kept 8 days. Times are unix seconds (UTC)
-- of the slot's start. Everything else (points, the band, issues, room) is
-- derived on read and never stored.

CREATE TABLE metrics_series (
  id      INTEGER PRIMARY KEY,
  app_id  TEXT NOT NULL,
  service TEXT NOT NULL,
  UNIQUE (app_id, service)
) STRICT;

-- The histogram bounds a row's bucket counts are for (seconds, comma-separated,
-- +Inf implied). A later change of Traefik's buckets adds a layout and never
-- makes an old row misread.
CREATE TABLE metrics_layouts (
  id     INTEGER PRIMARY KEY,
  bounds TEXT NOT NULL UNIQUE
) STRICT;

-- A series' samples go with it: DeleteApp deletes the series, this cascades.
CREATE TABLE metrics_samples (
  series             INTEGER NOT NULL REFERENCES metrics_series (id) ON DELETE CASCADE,
  res                INTEGER NOT NULL CHECK (res IN (60, 600)),
  ts                 INTEGER NOT NULL,
  minutes            INTEGER NOT NULL,   -- minute rows folded in (1 for res 60)
  http_secs          INTEGER,            -- seconds the HTTP counts cover; NULL: no HTTP numbers
  requests           INTEGER,
  errors             INTEGER,            -- 5xx
  layout             INTEGER REFERENCES metrics_layouts (id),
  buckets            TEXT,               -- per-bucket counts (not cumulative), +Inf last, trailing zeros cut
  cpu_mhz            INTEGER,            -- summed over the running copies; NULL: Nomad not read
  cpu_reserved_mhz   INTEGER,
  memory_mb          INTEGER,
  memory_limit_mb    INTEGER,
  memory_reserved_mb INTEGER,
  copies             INTEGER,
  restarts           INTEGER,            -- NULL: no baseline (the first minute after a start)
  PRIMARY KEY (series, res, ts)
) STRICT, WITHOUT ROWID;

CREATE TABLE metrics_host (
  node               TEXT NOT NULL,      -- Nomad node ID
  res                INTEGER NOT NULL CHECK (res IN (60, 600)),
  ts                 INTEGER NOT NULL,
  memory_used_mb     INTEGER,
  memory_reserved_mb INTEGER,
  memory_total_mb    INTEGER,
  cpu_used_mhz       INTEGER,
  cpu_reserved_mhz   INTEGER,
  cpu_total_mhz      INTEGER,
  cpu_cores          INTEGER,
  PRIMARY KEY (node, res, ts)
) STRICT, WITHOUT ROWID;

CREATE TABLE metrics_disks (
  node     TEXT NOT NULL,
  res      INTEGER NOT NULL CHECK (res IN (60, 600)),
  ts       INTEGER NOT NULL,
  mount    TEXT NOT NULL,
  holds    TEXT NOT NULL,                -- nomad, docker or nomad,docker
  used_mb  INTEGER NOT NULL,
  total_mb INTEGER NOT NULL,
  free_mb  INTEGER NOT NULL,             -- what a non-root writer gets
  PRIMARY KEY (node, res, ts, mount)
) STRICT, WITHOUT ROWID;

-- One row, written by the first tick ever and never moved.
CREATE TABLE metrics_state (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  started_at TEXT NOT NULL
) STRICT;

-- A sent problem remembers the level it last sent ("warning" or "failed"; empty
-- for the kinds that have none), so a disk that rises from warning to failed
-- sends one more message and a fall or a repeat sends none. sent_problems is
-- 0010's, which has shipped, so this is an ALTER; 0019 hasn't shipped.
ALTER TABLE sent_problems ADD COLUMN level TEXT NOT NULL DEFAULT '';
