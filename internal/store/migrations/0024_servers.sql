-- Feature 031: servers. One row per server of the cluster, including the one Caravana runs on.
-- A row is made when the owner asks for a join command (state 'waiting'), used up by the
-- server's join call ('joining') and ready once the four checks pass ('ready').
--
-- Times are RFC 3339 text in UTC with nine fraction digits, as everywhere; '' means not yet.
-- Only SHA-256 hashes of the join and node tokens are kept, never a token; the report is the
-- agent's last sync as JSON (versions, disk, reach), never a key or a certificate.

CREATE TABLE servers (
  idx            INTEGER PRIMARY KEY CHECK (idx BETWEEN 1 AND 63),  -- the index of node.md §1
  id             TEXT NOT NULL UNIQUE,          -- 26 lowercase base32, as other IDs; a join's handle
  name           TEXT NOT NULL DEFAULT '',      -- '' while a join that leaves the name to the machine waits
  pool           TEXT NOT NULL DEFAULT 'default',
  state          TEXT NOT NULL CHECK (state IN ('waiting', 'joining', 'ready')),
  first          INTEGER NOT NULL DEFAULT 0 CHECK (first IN (0, 1)),
  nomad_server   INTEGER NOT NULL DEFAULT 0 CHECK (nomad_server IN (0, 1)),  -- the role Caravana wants
  public_key     TEXT NOT NULL DEFAULT '',
  endpoint       TEXT NOT NULL DEFAULT '',      -- "ip:51820"
  join_hash      TEXT NOT NULL DEFAULT '',      -- SHA-256 hex of the join token; '' once used
  join_expires   TEXT NOT NULL DEFAULT '',
  token_hash     TEXT NOT NULL DEFAULT '',      -- SHA-256 hex of the node token
  created_at     TEXT NOT NULL,
  joined_at      TEXT NOT NULL DEFAULT '',
  ready_at       TEXT NOT NULL DEFAULT '',
  seen_at        TEXT NOT NULL DEFAULT '',      -- the agent's last sync
  report         TEXT NOT NULL DEFAULT '',      -- its last report, JSON
  cert_not_after TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX servers_name ON servers (name) WHERE name != '';
CREATE UNIQUE INDEX servers_join_hash ON servers (join_hash) WHERE join_hash != '';
CREATE UNIQUE INDEX servers_token_hash ON servers (token_hash) WHERE token_hash != '';
