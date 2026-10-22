-- Feature 021: which API token made a deploy.
--
-- The token's name at the time of the deploy (the Activity rail says "through
-- the API by ci-github"); NULL for a session, the webhook and every deployment
-- recorded before. A name, not a reference: revoking or renaming the token
-- later doesn't change what the history says.
ALTER TABLE deployments ADD COLUMN token_name TEXT;
