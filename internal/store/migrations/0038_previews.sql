-- Feature 049: previews. A preview is an environment named pr-<n>; what is kept
-- per project is how many may be open at once, how long an idle one lives, and
-- the bcrypt hash of the password their domains share. NULL means no password
-- (previews are public). A hash is not a secret (constitution VII); the password
-- itself is never stored.
ALTER TABLE projects ADD COLUMN preview_limit INTEGER NOT NULL DEFAULT 5;
ALTER TABLE projects ADD COLUMN preview_expire_days INTEGER NOT NULL DEFAULT 7;
ALTER TABLE projects ADD COLUMN preview_password_hash TEXT;
