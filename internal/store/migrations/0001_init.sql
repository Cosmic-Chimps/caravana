-- Apps are stored whole, as one JSON document each (app.App with its json
-- tags). schema_version says which document format it is.
CREATE TABLE apps (
  id             TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  document       TEXT NOT NULL,
  created_at     TEXT NOT NULL, -- RFC 3339, UTC, nanoseconds
  updated_at     TEXT NOT NULL
) STRICT;

-- One row per deploy. Final statuses (healthy, failed, reverted, superseded)
-- never change; the store enforces the allowed changes.
CREATE TABLE deployments (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL REFERENCES apps (id),
  trigger     TEXT NOT NULL,
  digests     TEXT NOT NULL,              -- JSON object: service name -> repo@sha256:...
  status      TEXT NOT NULL CHECK (status IN
                ('pending', 'running', 'healthy', 'failed', 'reverted', 'superseded')),
  job_version INTEGER,                    -- NULL until Nomad registers the job
  warnings    TEXT NOT NULL DEFAULT '[]', -- JSON array of {field, message}
  error       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
) STRICT;

CREATE INDEX deployments_by_app ON deployments (app_id, created_at DESC);
