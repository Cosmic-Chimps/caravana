-- Feature 035: app templates. Two things:
--
-- 1. Which template an app was installed from. The two columns sit beside the
--    app document, as project and environment do, so no revision moves and
--    nothing reads them to deploy: they are provenance only. Apps made before
--    this feature have empty text, which reads as "no template".
-- 2. The template catalog's one row: the owner's setting, and the newest
--    verified catalog fetched from the catalog repository, when there is one.
--    Nothing here holds a secret: a catalog is public text.
--
-- (Migrations 0026 and 0027 belong to branches not merged yet; the runner
-- applies any version it hasn't seen, so a gap is fine.)
-- Times are RFC 3339 text in UTC like every other table's; empty text is "never".
ALTER TABLE apps ADD COLUMN template_name TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN template_version TEXT NOT NULL DEFAULT '';

CREATE TABLE template_catalog (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  checking      INTEGER NOT NULL DEFAULT 1,     -- the owner's setting: 0 makes no request at all
  bundle        BLOB,                           -- the fetched catalog.json, verified; NULL until one is kept
  bundle_sum    TEXT NOT NULL DEFAULT '',       -- the SHA-256 (hex) of the kept bundle, or of the newest one seen
  version       TEXT NOT NULL DEFAULT '',       -- the kept bundle's version
  fetched_at    TEXT NOT NULL DEFAULT '',       -- when the kept bundle was fetched
  checked_at    TEXT NOT NULL DEFAULT '',       -- the last attempt, good or not
  checked_ok_at TEXT NOT NULL DEFAULT '',       -- the last attempt that was answered well
  error         TEXT NOT NULL DEFAULT ''        -- the last failure in Caravana's words; empty when it worked
) STRICT;
INSERT INTO template_catalog (id) VALUES (1);
