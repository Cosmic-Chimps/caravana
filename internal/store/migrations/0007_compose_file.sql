-- Feature 015: the Compose file an app was made from, kept so it can be edited.

-- The app's kept file: the JSON of the deployCompose request (compose, name,
-- services overlay) as it was last deployed, secret values never stored.
-- NULL when the app wasn't made from a file, or the file was dropped.
ALTER TABLE apps ADD COLUMN compose TEXT;

-- The kept file the app had when this deployment ran, same format. NULL for
-- deployments not made from a file and for those recorded before this feature.
ALTER TABLE deployments ADD COLUMN compose TEXT;
