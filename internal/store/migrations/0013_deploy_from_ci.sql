-- Feature 020: deploy from CI.

-- A deploy token is an API token that can only deploy one app name in one
-- project. Both columns are NULL for a full token, and both are set for a
-- deploy token (the service enforces it); the app need not exist yet.
ALTER TABLE api_tokens ADD COLUMN scope_project TEXT;
ALTER TABLE api_tokens ADD COLUMN scope_app TEXT;

-- Where a deploy came from, as JSON ({commit, ref, actor, run, repository},
-- app.Origin); NULL when the caller said nothing.
ALTER TABLE deployments ADD COLUMN origin TEXT;
