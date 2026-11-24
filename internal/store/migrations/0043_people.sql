-- Feature 053: people and roles. The one admin becomes the first person, the
-- owner, and sessions, API tokens and passkeys belong to a person.

CREATE TABLE people (
  id                  TEXT PRIMARY KEY,             -- 26 lowercase characters, not secret
  email               TEXT NOT NULL UNIQUE,         -- trimmed, lower case
  org_role            TEXT NOT NULL CHECK (org_role IN ('owner', 'admin', 'member')),
  state               TEXT NOT NULL CHECK (state IN ('invited', 'active')),
  password_hash       TEXT NOT NULL DEFAULT '',     -- '' while invited
  password_changed_at TEXT,
  password_signin     INTEGER NOT NULL DEFAULT 1,   -- 0: passkeys only (033)
  webauthn_handle     BLOB,                         -- 32 random bytes, made at the first passkey
  invite_hash         TEXT UNIQUE,                  -- hex SHA-256 of the cvin_ token; NULL once accepted
  invite_expires_at   TEXT,
  invited_by          TEXT,                         -- the inviter's email, kept as text
  ends_at             TEXT,                         -- NULL: no end (a guest has one)
  created_at          TEXT NOT NULL
) STRICT;

-- The admin, if there is one, is the owner. The ID is 13 random bytes in hex.
INSERT INTO people (id, email, org_role, state, password_hash, password_changed_at, password_signin, webauthn_handle, created_at)
  SELECT lower(hex(randomblob(13))), email, 'owner', 'active', password_hash, password_changed_at,
         password_signin, webauthn_handle, created_at
  FROM admin;

-- A role per project (members only) and, beside it, an override per environment.
CREATE TABLE project_roles (
  person_id TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
  project   TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  role      TEXT NOT NULL CHECK (role IN ('view', 'deploy', 'maintain')),
  ends_at   TEXT,
  PRIMARY KEY (person_id, project)
) STRICT;

CREATE TABLE environment_roles (
  person_id   TEXT NOT NULL,
  project     TEXT NOT NULL,
  environment TEXT NOT NULL,
  role        TEXT NOT NULL CHECK (role IN ('none', 'view', 'deploy', 'maintain')),
  PRIMARY KEY (person_id, project, environment),
  FOREIGN KEY (person_id, project) REFERENCES project_roles (person_id, project) ON DELETE CASCADE,
  FOREIGN KEY (project, environment) REFERENCES environments (project_id, name) ON DELETE CASCADE
) STRICT;

-- SQLite can't add a NOT NULL column that references another table, so the three
-- tables are made again with person_id. Nothing references them, so a plain DROP is
-- safe. Every existing row goes to the owner; with no admin there is no owner and the
-- tables are empty (a row can't exist before someone signed in), and the WHERE keeps
-- the copy from failing on a NULL person_id.
CREATE TABLE sessions_new (
  id_hash      TEXT PRIMARY KEY,       -- hex SHA-256 of the cookie value
  person_id    TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
  created_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  verified_at  TEXT
) STRICT;
INSERT INTO sessions_new (id_hash, person_id, created_at, last_seen_at, expires_at, verified_at)
  SELECT id_hash, (SELECT id FROM people WHERE org_role = 'owner' LIMIT 1), created_at, last_seen_at, expires_at, verified_at
  FROM sessions WHERE EXISTS (SELECT 1 FROM people);
DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;
CREATE INDEX sessions_by_person ON sessions (person_id);

CREATE TABLE api_tokens_new (
  id            TEXT PRIMARY KEY,
  person_id     TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  token_hash    TEXT NOT NULL UNIQUE,
  created_at    TEXT NOT NULL,
  last_used_at  TEXT,
  expires_at    TEXT,
  revoked_at    TEXT,
  scope_project TEXT,
  scope_app     TEXT
) STRICT;
INSERT INTO api_tokens_new (id, person_id, name, token_hash, created_at, last_used_at, expires_at, revoked_at, scope_project, scope_app)
  SELECT id, (SELECT id FROM people WHERE org_role = 'owner' LIMIT 1), name, token_hash, created_at, last_used_at,
         expires_at, revoked_at, scope_project, scope_app
  FROM api_tokens WHERE EXISTS (SELECT 1 FROM people);
DROP TABLE api_tokens;
ALTER TABLE api_tokens_new RENAME TO api_tokens;
CREATE INDEX api_tokens_name ON api_tokens (name);
CREATE INDEX api_tokens_by_person ON api_tokens (person_id);

CREATE TABLE passkeys_new (
  id                 TEXT PRIMARY KEY,
  person_id          TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
  user_handle        BLOB NOT NULL,
  credential_id      BLOB NOT NULL UNIQUE,
  rp_id              TEXT NOT NULL,
  public_key         BLOB NOT NULL,
  attestation_type   TEXT NOT NULL,
  attestation_format TEXT NOT NULL,
  aaguid             BLOB NOT NULL,
  sign_count         INTEGER NOT NULL,
  transports         TEXT NOT NULL,
  user_present       INTEGER NOT NULL,
  user_verified      INTEGER NOT NULL,
  backup_eligible    INTEGER NOT NULL,
  backup_state       INTEGER NOT NULL,
  name               TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  last_used_at       TEXT
) STRICT;
INSERT INTO passkeys_new (id, person_id, user_handle, credential_id, rp_id, public_key, attestation_type, attestation_format,
                          aaguid, sign_count, transports, user_present, user_verified, backup_eligible, backup_state,
                          name, created_at, last_used_at)
  SELECT id, (SELECT id FROM people WHERE org_role = 'owner' LIMIT 1), user_handle, credential_id, rp_id, public_key,
         attestation_type, attestation_format, aaguid, sign_count, transports, user_present, user_verified,
         backup_eligible, backup_state, name, created_at, last_used_at
  FROM passkeys WHERE EXISTS (SELECT 1 FROM people);
DROP TABLE passkeys;
ALTER TABLE passkeys_new RENAME TO passkeys;
CREATE INDEX passkeys_rp ON passkeys (rp_id);
CREATE INDEX passkeys_by_person ON passkeys (person_id);

DROP TABLE admin;

-- Who did it: the acting person's email (D9). NULL for rows from before and for
-- webhook, schedule and autoscale work.
ALTER TABLE deployments ADD COLUMN actor TEXT;
ALTER TABLE app_actions ADD COLUMN actor TEXT;
ALTER TABLE backups     ADD COLUMN actor TEXT;
ALTER TABLE restores    ADD COLUMN actor TEXT;
