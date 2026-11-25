-- Feature 056: dropping the earlier certificates an upload covers, from the console.
--
-- certificate_drops is one row per request to the ingress server's agent to remove from Traefik's store
-- the ACME certificates that uploaded certificates cover, and then to restart Traefik once. The agent
-- is handed the request in its sync answer and tells the outcome in its next report; the console then
-- restarts Traefik (status restarting) and calls it done once Traefik's task has started again.
--
-- The row holds only what the agent says in Caravana's own words: the names of what was dropped
-- (JSON array, "a.example.com" or "a.example.com+www.example.com" per certificate) and one sentence of
-- reason. Never a certificate, a key or an account. At most one drop is open (requested, running or
-- restarting) in the whole installation; a drop is deleted with its server, as a clean-up is.
--
-- Times are RFC 3339 text in UTC with nine fraction digits, like every other table's; a NULL time is
-- not known yet.

CREATE TABLE certificate_drops (
  id            TEXT PRIMARY KEY,                       -- 32 lowercase hex characters: the agent's request ID
  server_idx    INTEGER NOT NULL REFERENCES servers (idx) ON DELETE CASCADE,
  server_name   TEXT NOT NULL,                          -- the name at the time: the page's words
  status        TEXT NOT NULL CHECK (status IN ('requested', 'running', 'restarting', 'done', 'failed', 'lost')),
  dropped       TEXT NOT NULL DEFAULT '[]',             -- JSON array of strings, at most 100
  error         TEXT NOT NULL DEFAULT '',
  actor         TEXT NOT NULL DEFAULT '',               -- the person's email, '' for Caravana itself
  requested_at  TEXT NOT NULL,
  started_at    TEXT,                                   -- when the agent was handed it
  rewritten_at  TEXT,                                   -- when the agent said the store was rewritten
  finished_at   TEXT
) STRICT;
CREATE INDEX certificate_drops_by_time ON certificate_drops (requested_at);
CREATE UNIQUE INDEX certificate_drops_one_open ON certificate_drops ((1)) WHERE status IN ('requested', 'running', 'restarting');
