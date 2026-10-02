-- At most one webhook token per app. Only a hash is stored; the token itself
-- is shown once, when it's created.
CREATE TABLE webhook_tokens (
  app_id     TEXT PRIMARY KEY REFERENCES apps (id),
  token_hash TEXT NOT NULL, -- hex SHA-256 of the token
  created_at TEXT NOT NULL  -- RFC 3339, UTC
) STRICT;
