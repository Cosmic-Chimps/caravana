-- Feature 042: placement and drain.
--
-- servers.labels is the owner's labels for a server, a JSON object of name to value
-- ('{}' for none). Caravana writes them to the server's Nomad node as dynamic node
-- metadata and keeps the two equal.
--
-- server_moves records each "move work off this server": one row per request, written
-- when it starts and settled when it ends. It holds counts, the copies' app and service
-- names and Caravana's own sentences, never anything an app wrote. A move is deleted with
-- its server.

ALTER TABLE servers ADD COLUMN labels TEXT NOT NULL DEFAULT '{}';

CREATE TABLE server_moves (
  id          TEXT PRIMARY KEY,                 -- "mv_" + 26 lowercase base32
  server_idx  INTEGER NOT NULL REFERENCES servers (idx) ON DELETE CASCADE,
  state       TEXT NOT NULL CHECK (state IN ('moving', 'done', 'incomplete', 'cancelled')),
  started_at  TEXT NOT NULL,
  deadline_at TEXT NOT NULL,
  ended_at    TEXT NOT NULL DEFAULT '',
  moved       INTEGER NOT NULL DEFAULT 0,       -- copies that left the server
  left        INTEGER NOT NULL DEFAULT 0,       -- movable copies still there when it ended
  stays       TEXT NOT NULL DEFAULT '[]',       -- JSON [{app, service, reason}]: what never moves
  error       TEXT NOT NULL DEFAULT ''          -- one sentence in Caravana's words
) STRICT;
CREATE INDEX server_moves_server ON server_moves (server_idx, started_at);
