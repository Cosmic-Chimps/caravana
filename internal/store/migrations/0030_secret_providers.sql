-- Feature 037: secret providers, and the deployments a "Refresh secrets"
-- made.
--
-- A provider is where an app's secret references point ("from bella-prod").
-- Only what isn't secret is here: the credentials (API key, private key) live
-- in the Nomad Variable caravana/secret-providers/<name>, as the registries'
-- and the backup destination's do.
CREATE TABLE secret_providers (
  name         TEXT PRIMARY KEY,           -- a lowercase DNS label
  kind         TEXT NOT NULL CHECK (kind IN ('bella')),
  settings     TEXT NOT NULL,              -- JSON {url, project, environment}
  allowed      TEXT NOT NULL DEFAULT '[]', -- JSON list of "project" or "project/environment"; empty means every place
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  tested_at    TEXT,                       -- NULL until tested
  test_ok      INTEGER,                    -- 1 readable, 0 refused or unreachable
  test_message TEXT NOT NULL DEFAULT '',
  test_count   INTEGER NOT NULL DEFAULT 0  -- how many secrets a successful test could read
) STRICT;

-- A deployment made by Refresh secrets: the live version rolled out again
-- because a value changed in a provider.
ALTER TABLE deployments ADD COLUMN refresh INTEGER NOT NULL DEFAULT 0;
