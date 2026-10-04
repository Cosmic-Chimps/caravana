-- The one admin. id is always 1, so a second row can't exist (FR-001).
CREATE TABLE admin (
  id                  INTEGER PRIMARY KEY CHECK (id = 1),
  email               TEXT NOT NULL,   -- trimmed, lower case
  password_hash       TEXT NOT NULL,   -- pbkdf2-sha256$<iterations>$<salt>$<key> (R2)
  created_at          TEXT NOT NULL,   -- RFC 3339, UTC
  password_changed_at TEXT NOT NULL
) STRICT;

-- Signed-in browsers. Only the SHA-256 of the cookie value is stored (R6).
CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,       -- hex SHA-256 of the cookie value
  created_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,          -- written at most once a minute
  expires_at   TEXT NOT NULL           -- created_at + 7 days; idle limit is last_seen_at + 12 h
) STRICT;

-- API tokens. Only the SHA-256 of the token is stored (R10).
CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,       -- 26 lowercase base32 characters, not secret
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,   -- hex SHA-256 of the token
  created_at   TEXT NOT NULL,
  last_used_at TEXT,                   -- NULL until first used
  expires_at   TEXT,                   -- NULL: never
  revoked_at   TEXT                    -- NULL: not revoked
) STRICT;
CREATE INDEX api_tokens_name ON api_tokens (name);

-- Failed sign-ins per source address (IPv4 address or IPv6 /64), R8.
CREATE TABLE signin_limits (
  address      TEXT PRIMARY KEY,
  failures     INTEGER NOT NULL,
  window_start TEXT NOT NULL,
  locked_until TEXT                    -- NULL: not locked
) STRICT;
