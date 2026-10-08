-- Feature 011: rollback and export.

-- The app document each deployment deployed (app.App JSON, the apps.document
-- format, secret values never stored), so a past version can be deployed or
-- exported again. NULL for deployments recorded before this feature.
ALTER TABLE deployments ADD COLUMN definition TEXT;

-- For a redeploy of a past deployment, that deployment's ID. No foreign key:
-- an app's deployments are deleted in one statement, and the service checks
-- that the source belongs to the same app.
ALTER TABLE deployments ADD COLUMN source_deployment TEXT;
